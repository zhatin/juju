// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"time"

	"github.com/juju/errors"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/core/lease"
)

// singularSecretary implements lease.Secretary to restrict claims to either
// a lease for the controller or a specific model, holdable only by machine-tag
// strings.
//
// It would be nicer to have a single controller-level component managing all
// singular leases for all models -- and thus be able to validate that
// proposed holders really are env managers -- but the complexity of threading
// data from *two* states through a single api connection is excessive by
// comparison.
type singularSecretary struct {
	controllerUUID string
	modelUUID      string
}

// CheckLease is part of the lease.Secretary interface.
func (s singularSecretary) CheckLease(name string) error {
	if name != s.controllerUUID && name != s.modelUUID {
		return errors.New("expected controller or model UUID")
	}
	return nil
}

// CheckHolder is part of the lease.Secretary interface.
func (s singularSecretary) CheckHolder(name string) error {
	if _, err := names.ParseMachineTag(name); err != nil {
		return errors.New("expected machine tag")
	}
	return nil
}

// CheckDuration is part of the lease.Secretary interface.
func (s singularSecretary) CheckDuration(duration time.Duration) error {
	if duration <= 0 {
		return errors.NewNotValid(nil, "non-positive")
	}
	return nil
}

// SingularClaimer returns a lease.Claimer representing the exclusive right to
// manage the model.
func (st *State) SingularClaimer() lease.Claimer {
	return lazyLeaseClaimer{func() (lease.Claimer, error) {
		manager := st.workers.singularManager()
		return manager.Claimer(singularControllerNamespace, st.modelUUID())
	}}
}
