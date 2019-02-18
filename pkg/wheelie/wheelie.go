package wheelie

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/databus23/helm-diff/diff"
	"github.com/databus23/helm-diff/manifest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/helm"
	"k8s.io/helm/pkg/helm/portforwarder"
	"k8s.io/helm/pkg/kube"
	"k8s.io/helm/pkg/proto/hapi/release"
	storageerrors "k8s.io/helm/pkg/storage/errors"
)

// Wheelie uses the helm API to ensure a release is present, absent, or purged.
type Wheelie struct {
	Kubeconfig      string                 `json:"kubeconfig"`
	KubeContext     string                 `json:"kube_context"`
	Chart           string                 `json:"chart"`
	ChartVersion    string                 `json:"chart_version"`
	Values          map[string]interface{} `json:"values"`
	NoHooks         bool                   `json:"no_hooks"`
	NoCRDHook       bool                   `json:"no_crd_hook"`
	Timeout         int64                  `json:"timeout"`
	Release         string                 `json:"release"`
	Namespace       string                 `json:"namespace"`
	Wait            bool                   `json:"wait"`
	TillerNamespace string                 `json:"tiller_namespace"`
	TillerHost      string                 `json:"tiller_host"`
	TillerTimeout   int64                  `json:"tiller_timeout"`
}

// ForwardTillerPort creates a tunnel from localhost to the tiller pod. This should be
// called before any of the Ensure methods, unless the TillerHost field of the Wheelie
// struct has already been set. Calling this method will set the struct's TillerHost
// field to 127.0.0.1:<port>, where <port> is a selected local listen port.
func (w *Wheelie) ForwardTillerPort() error {
	config, err := kube.GetConfig(w.KubeContext, w.Kubeconfig).ClientConfig()
	if err != nil {
		return fmt.Errorf("could not get Kubernetes config for context %q: %s",
			w.KubeContext, err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("could not get Kubernetes client: %s", err)
	}
	forwarder, err := portforwarder.New(w.TillerNamespace, client, config)
	if err != nil {
		return err
	}
	w.TillerHost = fmt.Sprintf("127.0.0.1:%d", forwarder.Local)
	return nil
}

// EnsureReleasePresent ensures a release is present according to the following rules:
//
// If the release is not found, it is installed.
// If the release is found but in a deleted state, it is force updated.
// If the release is otherwise present, a dry-run update is performed, and the output
// is compared to the existing release.
// If there are differences, the update is performed without dry-run set.
func (w *Wheelie) EnsureReleasePresent() (string, bool, error) {
	chartPath := w.Chart

	chart, err := chartutil.Load(chartPath)
	if err != nil {
		return "", false, err
	}

	rawVals, err := json.Marshal(w.Values)
	if err != nil {
		return "", false, err
	}

	helmOptions := []helm.Option{
		helm.Host(w.TillerHost),
		helm.ConnectTimeout(w.TillerTimeout),
	}
	client := helm.NewClient(helmOptions...)

	releaseResponse, err := client.ReleaseContent(w.Release)
	if err != nil && strings.Contains(err.Error(), storageerrors.ErrReleaseNotFound(w.Release).Error()) {
		// Release doesn't exist, will install
		res, err := client.InstallReleaseFromChart(
			chart,
			w.Namespace,
			helm.ValueOverrides(rawVals),
			helm.ReleaseName(w.Release),
			helm.InstallDisableHooks(w.NoHooks),
			helm.InstallDisableCRDHook(w.NoCRDHook),
			helm.InstallTimeout(w.Timeout),
			helm.InstallWait(w.Wait))
		if err != nil {
			return "", false, err
		}
		return res.Release.Info.Description, true, nil
	}
	if releaseResponse.Release.Info.Status.Code == release.Status_DELETED {
		// Release exists in deleted state, will force update
		res, err := client.UpdateRelease(
			w.Release,
			chartPath,
			helm.UpdateValueOverrides(rawVals),
			helm.UpgradeDisableHooks(w.NoHooks),
			helm.UpgradeTimeout(w.Timeout),
			helm.UpgradeWait(w.Wait),
			helm.UpgradeForce(true))
		if err != nil {
			return "", false, err
		}
		return res.Release.Info.Description, true, nil
	}

	// Do a dry-run update to check the response for any differences between
	// desired and actual state.
	dryRunResponse, err := client.UpdateRelease(
		w.Release,
		chartPath,
		helm.UpdateValueOverrides(rawVals),
		helm.UpgradeDisableHooks(w.NoHooks),
		helm.UpgradeTimeout(w.Timeout),
		helm.UpgradeDryRun(true))
	if err != nil {
		return "", false, err
	}

	// Use helm-diff to check the difference between desired and actual state.
	var currentManifests, newManifests map[string]*manifest.MappingResult

	if w.NoHooks {
		currentManifests = manifest.Parse(
			releaseResponse.Release.Manifest, releaseResponse.Release.Namespace)
		newManifests = manifest.Parse(
			dryRunResponse.Release.Manifest, dryRunResponse.Release.Namespace)
	} else {
		currentManifests = manifest.ParseRelease(releaseResponse.Release)
		newManifests = manifest.ParseRelease(dryRunResponse.Release)
	}

	hasChanges := diff.DiffManifests(currentManifests, newManifests, []string{}, -1, os.Stderr)
	if hasChanges {
		res, err := client.UpdateRelease(
			w.Release,
			chartPath,
			helm.UpdateValueOverrides(rawVals),
			helm.UpgradeDisableHooks(w.NoHooks),
			helm.UpgradeTimeout(w.Timeout),
			helm.UpgradeWait(w.Wait))
		if err != nil {
			return "", false, err
		}
		return res.Release.Info.Description, true, nil
	}
	return "", false, nil
}

// EnsureReleaseAbsent deletes the helm release without the `DeletePurge` option set.
func (w *Wheelie) EnsureReleaseAbsent() (string, bool, error) {
	return w.ensureReleaseAbsent(false)
}

// EnsureReleasePurged deletes the helm release with the `DeletePurge` option set.
func (w *Wheelie) EnsureReleasePurged() (string, bool, error) {
	return w.ensureReleaseAbsent(true)
}

func (w *Wheelie) ensureReleaseAbsent(purge bool) (string, bool, error) {
	helmOptions := []helm.Option{
		helm.Host(w.TillerHost),
		helm.ConnectTimeout(w.TillerTimeout),
	}
	client := helm.NewClient(helmOptions...)

	releaseResponse, err := client.ReleaseContent(w.Release)
	if err != nil && strings.Contains(err.Error(), storageerrors.ErrReleaseNotFound(w.Release).Error()) {
		return "", false, nil
	}
	if releaseResponse.Release.Info.Status.Code == release.Status_DELETED && !purge {
		return "", false, nil
	}

	opts := []helm.DeleteOption{
		helm.DeleteDisableHooks(w.NoHooks),
		helm.DeletePurge(purge),
		helm.DeleteTimeout(w.Timeout),
	}
	_, err = client.DeleteRelease(w.Release, opts...)
	if err != nil {
		return "", false, err
	}
	msg := fmt.Sprintf("release %v deleted", w.Release)
	return msg, true, nil
}
