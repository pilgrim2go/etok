package controller

import (
	"github.com/leg100/terraform-operator/pkg/controller/command"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, command.Add)
}
