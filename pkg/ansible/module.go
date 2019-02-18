package ansible

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/cloudboss/wheelie/pkg/wheelie"
)

const (
	statePresent = "present"
	stateAbsent  = "absent"
	statePurged  = "purged"
)

// ModuleInput is the input passed by Ansible in the module declaration.
type ModuleInput struct {
	Kubeconfig      string                 `json:"kubeconfig"`
	Chart           string                 `json:"chart"`
	ChartVersion    string                 `json:"chart_version"`
	Values          map[string]interface{} `json:"values"`
	NoHooks         bool                   `json:"no_hooks"`
	NoCRDHook       bool                   `json:"no_crd_hook"`
	Timeout         int64                  `json:"timeout"`
	Release         string                 `json:"release"`
	Namespace       string                 `json:"namespace"`
	State           string                 `json:"state"`
	TillerNamespace string                 `json:"tiller_namespace"`
	Wait            bool                   `json:"wait"`
}

// ModuleOutput is the output from the module to Ansible.
type ModuleOutput struct {
	Msg        string           `json:"msg,omitempty"`
	Changed    bool             `json:"changed"`
	Failed     bool             `json:"failed"`
	Invocation ModuleInvocation `json:"invocation"`
}

// ModuleInvocation shows the input to the module, which is included in the
// output when running Ansible with extra verbosity.
type ModuleInvocation struct {
	ModuleArgs ModuleInput `json:"module_args"`
}

// HelmModule is the Ansible helm module.
type HelmModule struct {
	Input  ModuleInput
	Output ModuleOutput
}

// Run acts as the main function for executing the Ansible module.
func (m *HelmModule) Run() {
	nargs := len(os.Args)
	if nargs != 2 {
		m.fail(fmt.Sprintf("expected 2 arguments to module, got %d", nargs))
		return
	}

	inputFile := os.Args[1]

	text, err := ioutil.ReadFile(inputFile)
	if err != nil {
		m.fail(fmt.Sprintf("error reading input: %v", err))
		return
	}

	err = json.Unmarshal(text, &m.Input)
	if err != nil {
		m.fail(fmt.Sprintf("unable to parse input: %v", err))
		return
	}

	m.setDefaultInputs()

	w := wheelie.Wheelie{
		Kubeconfig:      m.Input.Kubeconfig,
		Chart:           m.Input.Chart,
		ChartVersion:    m.Input.ChartVersion,
		Values:          m.Input.Values,
		NoHooks:         m.Input.NoHooks,
		NoCRDHook:       m.Input.NoCRDHook,
		Timeout:         m.Input.Timeout,
		Release:         m.Input.Release,
		Namespace:       m.Input.Namespace,
		Wait:            m.Input.Wait,
		TillerNamespace: m.Input.TillerNamespace,
		TillerTimeout:   300,
	}

	err = w.ForwardTillerPort()
	if err != nil {
		m.fail(fmt.Sprintf("unable to forward tiller port: %v", err))
		return
	}

	var msg string
	var changed bool

	switch m.Input.State {
	case statePresent:
		msg, changed, err = w.EnsureReleasePresent()
	case stateAbsent:
		msg, changed, err = w.EnsureReleaseAbsent()
	case statePurged:
		msg, changed, err = w.EnsureReleasePurged()
	default:
		err = fmt.Errorf(`state must be one of '%s', '%s', or '%s'`,
			statePresent, stateAbsent, statePurged)
	}
	if err != nil {
		m.fail(err.Error())
		return
	}
	m.succeed(msg, changed)
}

func (m *HelmModule) setDefaultInputs() {
	if m.Input.State == "" {
		m.Input.State = "present"
	}
	if m.Input.Namespace == "" {
		m.Input.Namespace = "default"
	}
	if m.Input.Timeout == 0 {
		m.Input.Timeout = 300
	}
	if m.Input.Values == nil {
		m.Input.Values = make(map[string]interface{})
	}
	if m.Input.TillerNamespace == "" {
		m.Input.TillerNamespace = "kube-system"
	}
}

func (m *HelmModule) succeed(msg string, changed bool) {
	m.Output.Msg = msg
	m.Output.Changed = changed
	m.respondJSON()
}

func (m *HelmModule) fail(msg string) {
	m.Output.Failed = true
	m.Output.Msg = msg
	m.respondJSON()
}

func (m *HelmModule) respondJSON() {
	var responseBytes []byte
	var err error
	m.Output.Invocation.ModuleArgs = m.Input
	responseBytes, err = json.Marshal(m.Output)
	if err != nil {
		responseBytes, _ = json.Marshal(ModuleOutput{
			Msg: fmt.Sprintf("unexpected output: %v", err),
		})
	}
	fmt.Println(string(responseBytes))
}
