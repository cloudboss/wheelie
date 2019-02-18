package main

import (
	"github.com/cloudboss/wheelie/pkg/ansible"
)

func main() {
	module := ansible.HelmModule{}
	module.Run()
}
