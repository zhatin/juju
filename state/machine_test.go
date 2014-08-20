// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state_test

import (
	"sort"
	"strings"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/txn"
	"gopkg.in/mgo.v2/bson"
	gc "launchpad.net/gocheck"

	"github.com/juju/juju/constraints"
	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/network"
	"github.com/juju/juju/state"
	"github.com/juju/juju/state/api/params"
	"github.com/juju/juju/state/testing"
	coretesting "github.com/juju/juju/testing"
	"github.com/juju/juju/version"
)

type MachineSuite struct {
	ConnSuite
	machine0 *state.Machine
	machine  *state.Machine
}

var _ = gc.Suite(&MachineSuite{})

func (s *MachineSuite) SetUpTest(c *gc.C) {
	s.ConnSuite.SetUpTest(c)
	s.policy.GetConstraintsValidator = func(*config.Config) (constraints.Validator, error) {
		validator := constraints.NewValidator()
		validator.RegisterConflicts([]string{constraints.InstanceType}, []string{constraints.Mem})
		validator.RegisterUnsupported([]string{constraints.CpuPower})
		return validator, nil
	}
	var err error
	s.machine0, err = s.State.AddMachine("quantal", state.JobManageEnviron)
	c.Assert(err, gc.IsNil)
	s.machine, err = s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
}

func (s *MachineSuite) TestContainerDefaults(c *gc.C) {
	c.Assert(string(s.machine.ContainerType()), gc.Equals, "")
	containers, err := s.machine.Containers()
	c.Assert(err, gc.IsNil)
	c.Assert(containers, gc.DeepEquals, []string(nil))
}

func (s *MachineSuite) TestMachineJobFromParams(c *gc.C) {
	for stateMachineJob, paramsMachineJob := range state.JobNames {
		job, err := state.MachineJobFromParams(paramsMachineJob)
		c.Assert(err, gc.IsNil)
		c.Assert(job, gc.Equals, stateMachineJob)
	}
	_, err := state.MachineJobFromParams("invalid")
	c.Assert(err, gc.NotNil)
}

func (s *MachineSuite) TestParentId(c *gc.C) {
	parentId, ok := s.machine.ParentId()
	c.Assert(parentId, gc.Equals, "")
	c.Assert(ok, gc.Equals, false)
	container, err := s.State.AddMachineInsideMachine(state.MachineTemplate{
		Series: "quantal",
		Jobs:   []state.MachineJob{state.JobHostUnits},
	}, s.machine.Id(), instance.LXC)
	c.Assert(err, gc.IsNil)
	parentId, ok = container.ParentId()
	c.Assert(parentId, gc.Equals, s.machine.Id())
	c.Assert(ok, gc.Equals, true)
}

func (s *MachineSuite) TestMachineIsManager(c *gc.C) {
	c.Assert(s.machine0.IsManager(), jc.IsTrue)
	c.Assert(s.machine.IsManager(), jc.IsFalse)
}

func (s *MachineSuite) TestMachineIsManualBootstrap(c *gc.C) {
	cfg, err := s.State.EnvironConfig()
	c.Assert(err, gc.IsNil)
	c.Assert(cfg.Type(), gc.Not(gc.Equals), "null")
	c.Assert(s.machine.Id(), gc.Equals, "1")
	manual, err := s.machine0.IsManual()
	c.Assert(err, gc.IsNil)
	c.Assert(manual, jc.IsFalse)
	attrs := map[string]interface{}{"type": "null"}
	err = s.State.UpdateEnvironConfig(attrs, nil, nil)
	c.Assert(err, gc.IsNil)
	manual, err = s.machine0.IsManual()
	c.Assert(err, gc.IsNil)
	c.Assert(manual, jc.IsTrue)
}

func (s *MachineSuite) TestMachineIsManual(c *gc.C) {
	tests := []struct {
		instanceId instance.Id
		nonce      string
		isManual   bool
	}{
		{instanceId: "x", nonce: "y", isManual: false},
		{instanceId: "manual:", nonce: "y", isManual: false},
		{instanceId: "x", nonce: "manual:", isManual: true},
		{instanceId: "x", nonce: "manual:y", isManual: true},
		{instanceId: "x", nonce: "manual", isManual: false},
	}
	for _, test := range tests {
		m, err := s.State.AddOneMachine(state.MachineTemplate{
			Series:     "quantal",
			Jobs:       []state.MachineJob{state.JobHostUnits},
			InstanceId: test.instanceId,
			Nonce:      test.nonce,
		})
		c.Assert(err, gc.IsNil)
		isManual, err := m.IsManual()
		c.Assert(isManual, gc.Equals, test.isManual)
	}
}

func (s *MachineSuite) TestLifeJobManageEnviron(c *gc.C) {
	// A JobManageEnviron machine must never advance lifecycle.
	m := s.machine0
	err := m.Destroy()
	c.Assert(err, gc.ErrorMatches, "machine 0 is required by the environment")
	err = m.ForceDestroy()
	c.Assert(err, gc.ErrorMatches, "machine 0 is required by the environment")
	err = m.EnsureDead()
	c.Assert(err, gc.ErrorMatches, "machine 0 is required by the environment")
}

func (s *MachineSuite) TestLifeMachineWithContainer(c *gc.C) {
	// A machine hosting a container must not advance lifecycle.
	_, err := s.State.AddMachineInsideMachine(state.MachineTemplate{
		Series: "quantal",
		Jobs:   []state.MachineJob{state.JobHostUnits},
	}, s.machine.Id(), instance.LXC)
	c.Assert(err, gc.IsNil)
	err = s.machine.Destroy()
	c.Assert(err, gc.FitsTypeOf, &state.HasContainersError{})
	c.Assert(err, gc.ErrorMatches, `machine 1 is hosting containers "1/lxc/0"`)
	err1 := s.machine.EnsureDead()
	c.Assert(err1, gc.DeepEquals, err)
	c.Assert(s.machine.Life(), gc.Equals, state.Alive)
}

func (s *MachineSuite) TestLifeJobHostUnits(c *gc.C) {
	// A machine with an assigned unit must not advance lifecycle.
	svc := s.AddTestingService(c, "wordpress", s.AddTestingCharm(c, "wordpress"))
	unit, err := svc.AddUnit()
	c.Assert(err, gc.IsNil)
	err = unit.AssignToMachine(s.machine)
	c.Assert(err, gc.IsNil)
	err = s.machine.Destroy()
	c.Assert(err, gc.FitsTypeOf, &state.HasAssignedUnitsError{})
	c.Assert(err, gc.ErrorMatches, `machine 1 has unit "wordpress/0" assigned`)
	err1 := s.machine.EnsureDead()
	c.Assert(err1, gc.DeepEquals, err)
	c.Assert(s.machine.Life(), gc.Equals, state.Alive)

	// Once no unit is assigned, lifecycle can advance.
	err = unit.UnassignFromMachine()
	c.Assert(err, gc.IsNil)
	err = s.machine.Destroy()
	c.Assert(s.machine.Life(), gc.Equals, state.Dying)
	c.Assert(err, gc.IsNil)
	err = s.machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	c.Assert(s.machine.Life(), gc.Equals, state.Dead)

	// A machine that has never had units assigned can advance lifecycle.
	m, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = m.Destroy()
	c.Assert(err, gc.IsNil)
	c.Assert(m.Life(), gc.Equals, state.Dying)
	err = m.EnsureDead()
	c.Assert(err, gc.IsNil)
	c.Assert(m.Life(), gc.Equals, state.Dead)
}

func (s *MachineSuite) TestDestroyRemovePorts(c *gc.C) {
	svc := s.AddTestingService(c, "wordpress", s.AddTestingCharm(c, "wordpress"))
	unit, err := svc.AddUnit()
	c.Assert(err, gc.IsNil)
	err = unit.AssignToMachine(s.machine)
	c.Assert(err, gc.IsNil)
	err = unit.OpenPort("tcp", 8080)
	c.Assert(err, gc.IsNil)
	ports, err := state.GetPorts(s.State, s.machine.Id())
	c.Assert(ports, gc.NotNil)
	c.Assert(err, gc.IsNil)
	err = unit.UnassignFromMachine()
	c.Assert(err, gc.IsNil)
	err = s.machine.Destroy()
	c.Assert(err, gc.IsNil)
	err = s.machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = s.machine.Remove()
	c.Assert(err, gc.IsNil)
	// once the machine is destroyed, there should be no ports documents present for it
	ports, err = state.GetPorts(s.State, s.machine.Id())
	c.Assert(ports, gc.IsNil)
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
}

func (s *MachineSuite) TestDestroyAbort(c *gc.C) {
	defer state.SetBeforeHooks(c, s.State, func() {
		c.Assert(s.machine.Destroy(), gc.IsNil)
	}).Check()
	err := s.machine.Destroy()
	c.Assert(err, gc.IsNil)
}

func (s *MachineSuite) TestDestroyCancel(c *gc.C) {
	svc := s.AddTestingService(c, "wordpress", s.AddTestingCharm(c, "wordpress"))
	unit, err := svc.AddUnit()
	c.Assert(err, gc.IsNil)

	defer state.SetBeforeHooks(c, s.State, func() {
		c.Assert(unit.AssignToMachine(s.machine), gc.IsNil)
	}).Check()
	err = s.machine.Destroy()
	c.Assert(err, gc.FitsTypeOf, &state.HasAssignedUnitsError{})
}

func (s *MachineSuite) TestDestroyContention(c *gc.C) {
	svc := s.AddTestingService(c, "wordpress", s.AddTestingCharm(c, "wordpress"))
	unit, err := svc.AddUnit()
	c.Assert(err, gc.IsNil)

	perturb := txn.TestHook{
		Before: func() { c.Assert(unit.AssignToMachine(s.machine), gc.IsNil) },
		After:  func() { c.Assert(unit.UnassignFromMachine(), gc.IsNil) },
	}
	defer state.SetTestHooks(c, s.State, perturb, perturb, perturb).Check()

	err = s.machine.Destroy()
	c.Assert(err, gc.ErrorMatches, "machine 1 cannot advance lifecycle: state changing too quickly; try again soon")
}

func (s *MachineSuite) TestRemove(c *gc.C) {
	err := s.machine.Remove()
	c.Assert(err, gc.ErrorMatches, "cannot remove machine 1: machine is not dead")
	err = s.machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = s.machine.Remove()
	c.Assert(err, gc.IsNil)
	err = s.machine.Refresh()
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
	_, err = s.machine.HardwareCharacteristics()
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
	_, err = s.machine.Containers()
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
	networks, err := s.machine.RequestedNetworks()
	c.Assert(err, gc.IsNil)
	c.Assert(networks, gc.HasLen, 0)
	ifaces, err := s.machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 0)
	err = s.machine.Remove()
	c.Assert(err, gc.IsNil)
}

func (s *MachineSuite) TestHasVote(c *gc.C) {
	c.Assert(s.machine.HasVote(), jc.IsFalse)

	// Make another machine value so that
	// it won't have the cached HasVote value.
	m, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)

	err = s.machine.SetHasVote(true)
	c.Assert(err, gc.IsNil)
	c.Assert(s.machine.HasVote(), jc.IsTrue)
	c.Assert(m.HasVote(), jc.IsFalse)

	err = m.Refresh()
	c.Assert(err, gc.IsNil)
	c.Assert(m.HasVote(), jc.IsTrue)

	err = m.SetHasVote(false)
	c.Assert(err, gc.IsNil)
	c.Assert(m.HasVote(), jc.IsFalse)

	c.Assert(s.machine.HasVote(), jc.IsTrue)
	err = s.machine.Refresh()
	c.Assert(err, gc.IsNil)
	c.Assert(s.machine.HasVote(), jc.IsFalse)
}

func (s *MachineSuite) TestCannotDestroyMachineWithVote(c *gc.C) {
	err := s.machine.SetHasVote(true)
	c.Assert(err, gc.IsNil)

	// Make another machine value so that
	// it won't have the cached HasVote value.
	m, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)

	err = s.machine.Destroy()
	c.Assert(err, gc.ErrorMatches, "machine "+s.machine.Id()+" is a voting replica set member")

	err = m.Destroy()
	c.Assert(err, gc.ErrorMatches, "machine "+s.machine.Id()+" is a voting replica set member")
}

func (s *MachineSuite) TestRemoveAbort(c *gc.C) {
	err := s.machine.EnsureDead()
	c.Assert(err, gc.IsNil)

	defer state.SetBeforeHooks(c, s.State, func() {
		c.Assert(s.machine.Remove(), gc.IsNil)
	}).Check()
	err = s.machine.Remove()
	c.Assert(err, gc.IsNil)
}

func (s *MachineSuite) TestMachineSetAgentPresence(c *gc.C) {
	alive, err := s.machine.AgentPresence()
	c.Assert(err, gc.IsNil)
	c.Assert(alive, gc.Equals, false)

	pinger, err := s.machine.SetAgentPresence()
	c.Assert(err, gc.IsNil)
	c.Assert(pinger, gc.NotNil)
	defer pinger.Stop()

	s.State.StartSync()
	alive, err = s.machine.AgentPresence()
	c.Assert(err, gc.IsNil)
	c.Assert(alive, gc.Equals, true)
}

func (s *MachineSuite) TestTag(c *gc.C) {
	c.Assert(s.machine.Tag().String(), gc.Equals, "machine-1")
}

func (s *MachineSuite) TestSetMongoPassword(c *gc.C) {
	info := state.TestingMongoInfo()
	st, err := state.Open(info, state.TestingDialOpts(), state.Policy(nil))
	c.Assert(err, gc.IsNil)
	defer st.Close()
	// Turn on fully-authenticated mode.
	err = st.SetAdminMongoPassword("admin-secret")
	c.Assert(err, gc.IsNil)
	err = st.MongoSession().DB("admin").Login("admin", "admin-secret")
	c.Assert(err, gc.IsNil)

	// Set the password for the entity
	ent, err := st.Machine("0")
	c.Assert(err, gc.IsNil)
	err = ent.SetMongoPassword("foo")
	c.Assert(err, gc.IsNil)

	// Check that we cannot log in with the wrong password.
	info.Tag = ent.Tag()
	info.Password = "bar"
	err = tryOpenState(info)
	c.Assert(err, jc.Satisfies, errors.IsUnauthorized)

	// Check that we can log in with the correct password.
	info.Password = "foo"
	st1, err := state.Open(info, state.TestingDialOpts(), state.Policy(nil))
	c.Assert(err, gc.IsNil)
	defer st1.Close()

	// Change the password with an entity derived from the newly
	// opened and authenticated state.
	ent, err = st.Machine("0")
	c.Assert(err, gc.IsNil)
	err = ent.SetMongoPassword("bar")
	c.Assert(err, gc.IsNil)

	// Check that we cannot log in with the old password.
	info.Password = "foo"
	err = tryOpenState(info)
	c.Assert(err, jc.Satisfies, errors.IsUnauthorized)

	// Check that we can log in with the correct password.
	info.Password = "bar"
	err = tryOpenState(info)
	c.Assert(err, gc.IsNil)

	// Check that the administrator can still log in.
	info.Tag, info.Password = nil, "admin-secret"
	err = tryOpenState(info)
	c.Assert(err, gc.IsNil)

	// Remove the admin password so that the test harness can reset the state.
	err = st.SetAdminMongoPassword("")
	c.Assert(err, gc.IsNil)
}

func (s *MachineSuite) TestSetPassword(c *gc.C) {
	testSetPassword(c, func() (state.Authenticator, error) {
		return s.State.Machine(s.machine.Id())
	})
}

func (s *MachineSuite) TestSetAgentCompatPassword(c *gc.C) {
	e, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)
	testSetAgentCompatPassword(c, e)
}

func (s *MachineSuite) TestMachineWaitAgentPresence(c *gc.C) {
	alive, err := s.machine.AgentPresence()
	c.Assert(err, gc.IsNil)
	c.Assert(alive, gc.Equals, false)

	s.State.StartSync()
	err = s.machine.WaitAgentPresence(coretesting.ShortWait)
	c.Assert(err, gc.ErrorMatches, `waiting for agent of machine 1: still not alive after timeout`)

	pinger, err := s.machine.SetAgentPresence()
	c.Assert(err, gc.IsNil)

	s.State.StartSync()
	err = s.machine.WaitAgentPresence(coretesting.LongWait)
	c.Assert(err, gc.IsNil)

	alive, err = s.machine.AgentPresence()
	c.Assert(err, gc.IsNil)
	c.Assert(alive, gc.Equals, true)

	err = pinger.Kill()
	c.Assert(err, gc.IsNil)

	s.State.StartSync()
	alive, err = s.machine.AgentPresence()
	c.Assert(err, gc.IsNil)
	c.Assert(alive, gc.Equals, false)
}

func (s *MachineSuite) TestRequestedNetworks(c *gc.C) {
	// s.machine is created without requested networks, so check
	// they're empty when we read them.
	networks, err := s.machine.RequestedNetworks()
	c.Assert(err, gc.IsNil)
	c.Assert(networks, gc.HasLen, 0)

	// Now create a machine with networks and read them back.
	machine, err := s.State.AddOneMachine(state.MachineTemplate{
		Series:            "quantal",
		Jobs:              []state.MachineJob{state.JobHostUnits},
		Constraints:       constraints.MustParse("networks=mynet,^private-net,^logging"),
		RequestedNetworks: []string{"net1", "net2"},
	})
	c.Assert(err, gc.IsNil)
	networks, err = machine.RequestedNetworks()
	c.Assert(err, gc.IsNil)
	c.Assert(networks, jc.DeepEquals, []string{"net1", "net2"})
	cons, err := machine.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(cons.IncludeNetworks(), jc.DeepEquals, []string{"mynet"})
	c.Assert(cons.ExcludeNetworks(), jc.DeepEquals, []string{"private-net", "logging"})

	// Finally, networks should be removed with the machine.
	err = machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = machine.Remove()
	c.Assert(err, gc.IsNil)
	networks, err = machine.RequestedNetworks()
	c.Assert(err, gc.IsNil)
	c.Assert(networks, gc.HasLen, 0)
}

func addNetworkAndInterface(c *gc.C, st *state.State, machine *state.Machine,
	networkName, providerId, cidr string, vlanTag int, isVirtual bool,
	mac, ifaceName string,
) (*state.Network, *state.NetworkInterface) {
	net, err := st.AddNetwork(state.NetworkInfo{
		Name:       networkName,
		ProviderId: network.Id(providerId),
		CIDR:       cidr,
		VLANTag:    vlanTag,
	})
	c.Assert(err, gc.IsNil)
	iface, err := machine.AddNetworkInterface(state.NetworkInterfaceInfo{
		MACAddress:    mac,
		InterfaceName: ifaceName,
		NetworkName:   networkName,
		IsVirtual:     isVirtual,
	})
	c.Assert(err, gc.IsNil)
	return net, iface
}

func (s *MachineSuite) TestNetworks(c *gc.C) {
	// s.machine is created without networks, so check
	// they're empty when we read them.
	nets, err := s.machine.Networks()
	c.Assert(err, gc.IsNil)
	c.Assert(nets, gc.HasLen, 0)

	// Now create a testing machine with requested networks, because
	// Networks() uses them to determine which networks are bound to
	// the machine.
	machine, err := s.State.AddOneMachine(state.MachineTemplate{
		Series:            "quantal",
		Jobs:              []state.MachineJob{state.JobHostUnits},
		RequestedNetworks: []string{"net1", "net2"},
	})
	c.Assert(err, gc.IsNil)

	net1, _ := addNetworkAndInterface(
		c, s.State, machine,
		"net1", "net1", "0.1.2.0/24", 0, false,
		"aa:bb:cc:dd:ee:f0", "eth0")
	net2, _ := addNetworkAndInterface(
		c, s.State, machine,
		"net2", "net2", "0.2.2.0/24", 0, false,
		"aa:bb:cc:dd:ee:f1", "eth1")

	nets, err = machine.Networks()
	c.Assert(err, gc.IsNil)
	c.Assert(nets, jc.DeepEquals, []*state.Network{net1, net2})
}

func (s *MachineSuite) TestMachineNetworkInterfaces(c *gc.C) {
	// s.machine is created without network interfaces, so check
	// they're empty when we read them.
	ifaces, err := s.machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 0)

	machine, err := s.State.AddOneMachine(state.MachineTemplate{
		Series:            "quantal",
		Jobs:              []state.MachineJob{state.JobHostUnits},
		RequestedNetworks: []string{"net1", "vlan42", "net2"},
	})
	c.Assert(err, gc.IsNil)

	// And a few networks and NICs.
	_, iface0 := addNetworkAndInterface(
		c, s.State, machine,
		"net1", "net1", "0.1.2.0/24", 0, false,
		"aa:bb:cc:dd:ee:f0", "eth0")
	_, iface1 := addNetworkAndInterface(
		c, s.State, machine,
		"vlan42", "vlan42", "0.1.2.0/30", 42, true,
		"aa:bb:cc:dd:ee:f1", "eth0.42")
	_, iface2 := addNetworkAndInterface(
		c, s.State, machine,
		"net2", "net2", "0.2.2.0/24", 0, false,
		"aa:bb:cc:dd:ee:f2", "eth1")

	ifaces, err = machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, jc.DeepEquals, []*state.NetworkInterface{
		iface0, iface1, iface2,
	})

	// Make sure interfaces get removed with the machine.
	err = machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = machine.Remove()
	c.Assert(err, gc.IsNil)
	ifaces, err = machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 0)
}

var addNetworkInterfaceErrorsTests = []struct {
	args         state.NetworkInterfaceInfo
	beforeAdding func(*gc.C, *state.Machine)
	expectErr    string
}{{
	state.NetworkInterfaceInfo{"", "eth1", "net1", false, false},
	nil,
	`cannot add network interface "eth1" to machine "2": MAC address must be not empty`,
}, {
	state.NetworkInterfaceInfo{"invalid", "eth1", "net1", false, false},
	nil,
	`cannot add network interface "eth1" to machine "2": invalid MAC address: invalid`,
}, {
	state.NetworkInterfaceInfo{"aa:bb:cc:dd:ee:f0", "eth1", "net1", false, false},
	nil,
	`cannot add network interface "eth1" to machine "2": MAC address "aa:bb:cc:dd:ee:f0" on network "net1" already exists`,
}, {
	state.NetworkInterfaceInfo{"aa:bb:cc:dd:ee:ff", "", "net1", false, false},
	nil,
	`cannot add network interface "" to machine "2": interface name must be not empty`,
}, {
	state.NetworkInterfaceInfo{"aa:bb:cc:dd:ee:ff", "eth0", "net1", false, false},
	nil,
	`cannot add network interface "eth0" to machine "2": "eth0" on machine "2" already exists`,
}, {
	state.NetworkInterfaceInfo{"aa:bb:cc:dd:ee:ff", "eth1", "missing", false, false},
	nil,
	`cannot add network interface "eth1" to machine "2": network "missing" not found`,
}, {
	state.NetworkInterfaceInfo{"aa:bb:cc:dd:ee:f1", "eth1", "net1", false, false},
	func(c *gc.C, m *state.Machine) {
		c.Check(m.EnsureDead(), gc.IsNil)
	},
	`cannot add network interface "eth1" to machine "2": machine is not alive`,
}, {
	state.NetworkInterfaceInfo{"aa:bb:cc:dd:ee:f1", "eth1", "net1", false, false},
	func(c *gc.C, m *state.Machine) {
		c.Check(m.Remove(), gc.IsNil)
	},
	`cannot add network interface "eth1" to machine "2": machine 2 not found`,
}}

func (s *MachineSuite) TestAddNetworkInterfaceErrors(c *gc.C) {
	machine, err := s.State.AddOneMachine(state.MachineTemplate{
		Series:            "quantal",
		Jobs:              []state.MachineJob{state.JobHostUnits},
		RequestedNetworks: []string{"net1"},
	})
	c.Assert(err, gc.IsNil)
	addNetworkAndInterface(
		c, s.State, machine,
		"net1", "provider-net1", "0.1.2.0/24", 0, false,
		"aa:bb:cc:dd:ee:f0", "eth0")
	ifaces, err := machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 1)

	for i, test := range addNetworkInterfaceErrorsTests {
		c.Logf("test %d: %#v", i, test.args)

		if test.beforeAdding != nil {
			test.beforeAdding(c, machine)
		}

		_, err = machine.AddNetworkInterface(test.args)
		c.Check(err, gc.ErrorMatches, test.expectErr)
		if strings.Contains(test.expectErr, "not found") {
			c.Check(err, jc.Satisfies, errors.IsNotFound)
		}
		if strings.Contains(test.expectErr, "already exists") {
			c.Check(err, jc.Satisfies, errors.IsAlreadyExists)
		}
	}
}

func (s *MachineSuite) TestMachineInstanceId(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = s.machines.Update(
		bson.D{{"_id", machine.Id()}},
		bson.D{{"$set", bson.D{{"instanceid", "spaceship/0"}}}},
	)
	c.Assert(err, gc.IsNil)

	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	iid, err := machine.InstanceId()
	c.Assert(err, gc.IsNil)
	c.Assert(iid, gc.Equals, instance.Id("spaceship/0"))
}

func (s *MachineSuite) TestMachineInstanceIdCorrupt(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = s.machines.Update(
		bson.D{{"_id", machine.Id()}},
		bson.D{{"$set", bson.D{{"instanceid", bson.D{{"foo", "bar"}}}}}},
	)
	c.Assert(err, gc.IsNil)

	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	iid, err := machine.InstanceId()
	c.Assert(err, jc.Satisfies, state.IsNotProvisionedError)
	c.Assert(iid, gc.Equals, instance.Id(""))
}

func (s *MachineSuite) TestMachineInstanceIdMissing(c *gc.C) {
	iid, err := s.machine.InstanceId()
	c.Assert(err, jc.Satisfies, state.IsNotProvisionedError)
	c.Assert(string(iid), gc.Equals, "")
}

func (s *MachineSuite) TestMachineInstanceIdBlank(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	err = s.machines.Update(
		bson.D{{"_id", machine.Id()}},
		bson.D{{"$set", bson.D{{"instanceid", ""}}}},
	)
	c.Assert(err, gc.IsNil)

	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	iid, err := machine.InstanceId()
	c.Assert(err, jc.Satisfies, state.IsNotProvisionedError)
	c.Assert(string(iid), gc.Equals, "")
}

func (s *MachineSuite) TestMachineSetProvisionedUpdatesCharacteristics(c *gc.C) {
	// Before provisioning, there is no hardware characteristics.
	_, err := s.machine.HardwareCharacteristics()
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
	arch := "amd64"
	mem := uint64(4096)
	expected := &instance.HardwareCharacteristics{
		Arch: &arch,
		Mem:  &mem,
	}
	err = s.machine.SetProvisioned("umbrella/0", "fake_nonce", expected)
	c.Assert(err, gc.IsNil)
	md, err := s.machine.HardwareCharacteristics()
	c.Assert(err, gc.IsNil)
	c.Assert(*md, gc.DeepEquals, *expected)

	// Reload machine and check again.
	err = s.machine.Refresh()
	c.Assert(err, gc.IsNil)
	md, err = s.machine.HardwareCharacteristics()
	c.Assert(err, gc.IsNil)
	c.Assert(*md, gc.DeepEquals, *expected)
}

func (s *MachineSuite) TestMachineSetCheckProvisioned(c *gc.C) {
	// Check before provisioning.
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, false)

	// Either one should not be empty.
	err := s.machine.SetProvisioned("umbrella/0", "", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set instance data for machine "1": instance id and nonce cannot be empty`)
	err = s.machine.SetProvisioned("", "fake_nonce", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set instance data for machine "1": instance id and nonce cannot be empty`)
	err = s.machine.SetProvisioned("", "", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set instance data for machine "1": instance id and nonce cannot be empty`)

	err = s.machine.SetProvisioned("umbrella/0", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)

	m, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)
	id, err := m.InstanceId()
	c.Assert(err, gc.IsNil)
	c.Assert(string(id), gc.Equals, "umbrella/0")
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, true)
	// Clear the deprecated machineDoc InstanceId attribute and ensure that CheckProvisioned()
	// still works as expected with the new data model.
	state.SetMachineInstanceId(s.machine, "")
	id, err = s.machine.InstanceId()
	c.Assert(err, gc.IsNil)
	c.Assert(string(id), gc.Equals, "umbrella/0")
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, true)

	// Try it twice, it should fail.
	err = s.machine.SetProvisioned("doesn't-matter", "phony", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set instance data for machine "1": already set`)

	// Check it with invalid nonce.
	c.Assert(s.machine.CheckProvisioned("not-really"), gc.Equals, false)
}

func (s *MachineSuite) TestMachineSetInstanceInfoFailureDoesNotProvision(c *gc.C) {
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, false)
	invalidNetworks := []state.NetworkInfo{{Name: ""}}
	invalidInterfaces := []state.NetworkInterfaceInfo{{MACAddress: ""}}
	err := s.machine.SetInstanceInfo("umbrella/0", "fake_nonce", nil, invalidNetworks, nil)
	c.Assert(err, gc.ErrorMatches, `cannot add network "": name must be not empty`)
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, false)
	err = s.machine.SetInstanceInfo("umbrella/0", "fake_nonce", nil, nil, invalidInterfaces)
	c.Assert(err, gc.ErrorMatches, `cannot add network interface "" to machine "1": MAC address must be not empty`)
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, false)
}

func (s *MachineSuite) TestMachineSetInstanceInfoSuccess(c *gc.C) {
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, false)
	networks := []state.NetworkInfo{
		{Name: "net1", ProviderId: "net1", CIDR: "0.1.2.0/24", VLANTag: 0},
	}
	interfaces := []state.NetworkInterfaceInfo{
		{MACAddress: "aa:bb:cc:dd:ee:ff", NetworkName: "net1", InterfaceName: "eth0", IsVirtual: false},
	}
	err := s.machine.SetInstanceInfo("umbrella/0", "fake_nonce", nil, networks, interfaces)
	c.Assert(err, gc.IsNil)
	c.Assert(s.machine.CheckProvisioned("fake_nonce"), gc.Equals, true)
	network, err := s.State.Network(networks[0].Name)
	c.Assert(err, gc.IsNil)
	c.Check(network.Name(), gc.Equals, networks[0].Name)
	c.Check(network.ProviderId(), gc.Equals, networks[0].ProviderId)
	c.Check(network.VLANTag(), gc.Equals, networks[0].VLANTag)
	c.Check(network.CIDR(), gc.Equals, networks[0].CIDR)
	ifaces, err := s.machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 1)
	c.Check(ifaces[0].InterfaceName(), gc.Equals, interfaces[0].InterfaceName)
	c.Check(ifaces[0].NetworkName(), gc.Equals, interfaces[0].NetworkName)
	c.Check(ifaces[0].MACAddress(), gc.Equals, interfaces[0].MACAddress)
	c.Check(ifaces[0].MachineTag(), gc.Equals, s.machine.Tag().String())
	c.Check(ifaces[0].IsVirtual(), gc.Equals, interfaces[0].IsVirtual)
}

func (s *MachineSuite) TestMachineSetProvisionedWhenNotAlive(c *gc.C) {
	testWhenDying(c, s.machine, notAliveErr, notAliveErr, func() error {
		return s.machine.SetProvisioned("umbrella/0", "fake_nonce", nil)
	})
}

func (s *MachineSuite) TestMachineSetInstanceStatus(c *gc.C) {
	// Machine needs to be provisioned first.
	err := s.machine.SetProvisioned("umbrella/0", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)

	err = s.machine.SetInstanceStatus("ALIVE")
	c.Assert(err, gc.IsNil)

	// Reload machine and check result.
	err = s.machine.Refresh()
	c.Assert(err, gc.IsNil)
	status, err := s.machine.InstanceStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.DeepEquals, "ALIVE")
}

func (s *MachineSuite) TestNotProvisionedMachineSetInstanceStatus(c *gc.C) {
	err := s.machine.SetInstanceStatus("ALIVE")
	c.Assert(err, gc.ErrorMatches, ".* not provisioned")
}

func (s *MachineSuite) TestNotProvisionedMachineInstanceStatus(c *gc.C) {
	_, err := s.machine.InstanceStatus()
	c.Assert(err, jc.Satisfies, state.IsNotProvisionedError)
}

// SCHEMACHANGE
func (s *MachineSuite) TestInstanceStatusOldSchema(c *gc.C) {
	// Machine needs to be provisioned first.
	err := s.machine.SetProvisioned("umbrella/0", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)

	// Remove the InstanceId from instanceData doc to simulate an old schema.
	state.ClearInstanceDocId(c, s.machine)

	err = s.machine.SetInstanceStatus("ALIVE")
	c.Assert(err, gc.IsNil)

	// Reload machine and check result.
	err = s.machine.Refresh()
	c.Assert(err, gc.IsNil)
	status, err := s.machine.InstanceStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.DeepEquals, "ALIVE")
}

func (s *MachineSuite) TestMachineRefresh(c *gc.C) {
	m0, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	oldTools, _ := m0.AgentTools()
	m1, err := s.State.Machine(m0.Id())
	c.Assert(err, gc.IsNil)
	err = m0.SetAgentVersion(version.MustParseBinary("0.0.3-quantal-amd64"))
	c.Assert(err, gc.IsNil)
	newTools, _ := m0.AgentTools()

	m1Tools, _ := m1.AgentTools()
	c.Assert(m1Tools, gc.DeepEquals, oldTools)
	err = m1.Refresh()
	c.Assert(err, gc.IsNil)
	m1Tools, _ = m1.AgentTools()
	c.Assert(*m1Tools, gc.Equals, *newTools)

	err = m0.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = m0.Remove()
	c.Assert(err, gc.IsNil)
	err = m0.Refresh()
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
}

func (s *MachineSuite) TestRefreshWhenNotAlive(c *gc.C) {
	// Refresh should work regardless of liveness status.
	testWhenDying(c, s.machine, noErr, noErr, func() error {
		return s.machine.Refresh()
	})
}

func (s *MachineSuite) TestMachinePrincipalUnits(c *gc.C) {
	// Check that Machine.Units works correctly.

	// Make three machines, three services and three units for each service;
	// variously assign units to machines and check that Machine.Units
	// tells us the right thing.

	m1 := s.machine
	m2, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	m3, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)

	dummy := s.AddTestingCharm(c, "dummy")
	logging := s.AddTestingCharm(c, "logging")
	s0 := s.AddTestingService(c, "s0", dummy)
	s1 := s.AddTestingService(c, "s1", dummy)
	s2 := s.AddTestingService(c, "s2", dummy)
	s3 := s.AddTestingService(c, "s3", logging)

	units := make([][]*state.Unit, 4)
	for i, svc := range []*state.Service{s0, s1, s2} {
		units[i] = make([]*state.Unit, 3)
		for j := range units[i] {
			units[i][j], err = svc.AddUnit()
			c.Assert(err, gc.IsNil)
		}
	}
	// Add the logging units subordinate to the s2 units.
	eps, err := s.State.InferEndpoints([]string{"s2", "s3"})
	c.Assert(err, gc.IsNil)
	rel, err := s.State.AddRelation(eps...)
	c.Assert(err, gc.IsNil)
	for _, u := range units[2] {
		ru, err := rel.Unit(u)
		c.Assert(err, gc.IsNil)
		err = ru.EnterScope(nil)
		c.Assert(err, gc.IsNil)
	}
	units[3], err = s3.AllUnits()
	c.Assert(err, gc.IsNil)

	assignments := []struct {
		machine      *state.Machine
		units        []*state.Unit
		subordinates []*state.Unit
	}{
		{m1, []*state.Unit{units[0][0]}, nil},
		{m2, []*state.Unit{units[0][1], units[1][0], units[1][1], units[2][0]}, []*state.Unit{units[3][0]}},
		{m3, []*state.Unit{units[2][2]}, []*state.Unit{units[3][2]}},
	}

	for _, a := range assignments {
		for _, u := range a.units {
			err := u.AssignToMachine(a.machine)
			c.Assert(err, gc.IsNil)
		}
	}

	for i, a := range assignments {
		c.Logf("test %d", i)
		got, err := a.machine.Units()
		c.Assert(err, gc.IsNil)
		expect := sortedUnitNames(append(a.units, a.subordinates...))
		c.Assert(sortedUnitNames(got), gc.DeepEquals, expect)
	}
}

func sortedUnitNames(units []*state.Unit) []string {
	names := make([]string, len(units))
	for i, u := range units {
		names[i] = u.Name()
	}
	sort.Strings(names)
	return names
}

func (s *MachineSuite) assertMachineDirtyAfterAddingUnit(c *gc.C) (*state.Machine, *state.Service, *state.Unit) {
	m, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(m.Clean(), gc.Equals, true)

	svc := s.AddTestingService(c, "wordpress", s.AddTestingCharm(c, "wordpress"))
	unit, err := svc.AddUnit()
	c.Assert(err, gc.IsNil)
	err = unit.AssignToMachine(m)
	c.Assert(err, gc.IsNil)
	c.Assert(m.Clean(), gc.Equals, false)
	return m, svc, unit
}

func (s *MachineSuite) TestMachineDirtyAfterAddingUnit(c *gc.C) {
	s.assertMachineDirtyAfterAddingUnit(c)
}

func (s *MachineSuite) TestMachineDirtyAfterUnassigningUnit(c *gc.C) {
	m, _, unit := s.assertMachineDirtyAfterAddingUnit(c)
	err := unit.UnassignFromMachine()
	c.Assert(err, gc.IsNil)
	c.Assert(m.Clean(), gc.Equals, false)
}

func (s *MachineSuite) TestMachineDirtyAfterRemovingUnit(c *gc.C) {
	m, svc, unit := s.assertMachineDirtyAfterAddingUnit(c)
	err := unit.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = unit.Remove()
	c.Assert(err, gc.IsNil)
	err = svc.Destroy()
	c.Assert(err, gc.IsNil)
	c.Assert(m.Clean(), gc.Equals, false)
}

func (s *MachineSuite) TestWatchMachine(c *gc.C) {
	w := s.machine.Watch()
	defer testing.AssertStop(c, w)

	// Initial event.
	wc := testing.NewNotifyWatcherC(c, s.State, w)
	wc.AssertOneChange()

	// Make one change (to a separate instance), check one event.
	machine, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)
	err = machine.SetProvisioned("m-foo", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Make two changes, check one event.
	err = machine.SetAgentVersion(version.MustParseBinary("0.0.3-quantal-amd64"))
	c.Assert(err, gc.IsNil)
	err = machine.Destroy()
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Stop, check closed.
	testing.AssertStop(c, w)
	wc.AssertClosed()

	// Remove machine, start new watch, check single event.
	err = machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = machine.Remove()
	c.Assert(err, gc.IsNil)
	w = s.machine.Watch()
	defer testing.AssertStop(c, w)
	testing.NewNotifyWatcherC(c, s.State, w).AssertOneChange()
}

func (s *MachineSuite) TestWatchDiesOnStateClose(c *gc.C) {
	// This test is testing logic in watcher.entityWatcher, which
	// is also used by:
	//  Machine.WatchHardwareCharacteristics
	//  Service.Watch
	//  Unit.Watch
	//  State.WatchForEnvironConfigChanges
	//  Unit.WatchConfigSettings
	testWatcherDiesWhenStateCloses(c, func(c *gc.C, st *state.State) waiter {
		m, err := st.Machine(s.machine.Id())
		c.Assert(err, gc.IsNil)
		w := m.Watch()
		<-w.Changes()
		return w
	})
}

func (s *MachineSuite) TestWatchPrincipalUnits(c *gc.C) {
	// Start a watch on an empty machine; check no units reported.
	w := s.machine.WatchPrincipalUnits()
	defer testing.AssertStop(c, w)
	wc := testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChange()
	wc.AssertNoChange()

	// Change machine, and create a unit independently; no change.
	err := s.machine.SetProvisioned("cheese", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()
	mysql := s.AddTestingService(c, "mysql", s.AddTestingCharm(c, "mysql"))
	mysql0, err := mysql.AddUnit()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Assign that unit (to a separate machine instance); change detected.
	machine, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)
	err = mysql0.AssignToMachine(machine)
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/0")
	wc.AssertNoChange()

	// Change the unit; no change.
	err = mysql0.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Assign another unit and make the first Dying; check both changes detected.
	mysql1, err := mysql.AddUnit()
	c.Assert(err, gc.IsNil)
	err = mysql1.AssignToMachine(machine)
	c.Assert(err, gc.IsNil)
	err = mysql0.Destroy()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/0", "mysql/1")
	wc.AssertNoChange()

	// Add a subordinate to the Alive unit; no change.
	logging := s.AddTestingService(c, "logging", s.AddTestingCharm(c, "logging"))
	eps, err := s.State.InferEndpoints([]string{"mysql", "logging"})
	c.Assert(err, gc.IsNil)
	rel, err := s.State.AddRelation(eps...)
	c.Assert(err, gc.IsNil)
	mysqlru1, err := rel.Unit(mysql1)
	c.Assert(err, gc.IsNil)
	err = mysqlru1.EnterScope(nil)
	c.Assert(err, gc.IsNil)
	logging0, err := logging.Unit("logging/0")
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Change the subordinate; no change.
	err = logging0.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Make the Dying unit Dead; change detected.
	err = mysql0.EnsureDead()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/0")
	wc.AssertNoChange()

	// Stop watcher; check Changes chan closed.
	testing.AssertStop(c, w)
	wc.AssertClosed()

	// Start a fresh watcher; check both principals reported.
	w = s.machine.WatchPrincipalUnits()
	defer testing.AssertStop(c, w)
	wc = testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChange("mysql/0", "mysql/1")
	wc.AssertNoChange()

	// Remove the Dead unit; no change.
	err = mysql0.Remove()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Destroy the subordinate; no change.
	err = logging0.Destroy()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Unassign the unit; check change.
	err = mysql1.UnassignFromMachine()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/1")
	wc.AssertNoChange()
}

func (s *MachineSuite) TestWatchPrincipalUnitsDiesOnStateClose(c *gc.C) {
	// This test is testing logic in watcher.unitsWatcher, which
	// is also used by Unit.WatchSubordinateUnits.
	testWatcherDiesWhenStateCloses(c, func(c *gc.C, st *state.State) waiter {
		m, err := st.Machine(s.machine.Id())
		c.Assert(err, gc.IsNil)
		w := m.WatchPrincipalUnits()
		<-w.Changes()
		return w
	})
}

func (s *MachineSuite) TestWatchUnits(c *gc.C) {
	// Start a watch on an empty machine; check no units reported.
	w := s.machine.WatchUnits()
	defer testing.AssertStop(c, w)
	wc := testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChange()
	wc.AssertNoChange()

	// Change machine; no change.
	err := s.machine.SetProvisioned("cheese", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Assign a unit (to a separate instance); change detected.
	mysql := s.AddTestingService(c, "mysql", s.AddTestingCharm(c, "mysql"))
	mysql0, err := mysql.AddUnit()
	c.Assert(err, gc.IsNil)
	machine, err := s.State.Machine(s.machine.Id())
	c.Assert(err, gc.IsNil)
	err = mysql0.AssignToMachine(machine)
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/0")
	wc.AssertNoChange()

	// Change the unit; no change.
	err = mysql0.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Assign another unit and make the first Dying; check both changes detected.
	mysql1, err := mysql.AddUnit()
	c.Assert(err, gc.IsNil)
	err = mysql1.AssignToMachine(machine)
	c.Assert(err, gc.IsNil)
	err = mysql0.Destroy()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/0", "mysql/1")
	wc.AssertNoChange()

	// Add a subordinate to the Alive unit; change detected.
	logging := s.AddTestingService(c, "logging", s.AddTestingCharm(c, "logging"))
	eps, err := s.State.InferEndpoints([]string{"mysql", "logging"})
	c.Assert(err, gc.IsNil)
	rel, err := s.State.AddRelation(eps...)
	c.Assert(err, gc.IsNil)
	mysqlru1, err := rel.Unit(mysql1)
	c.Assert(err, gc.IsNil)
	err = mysqlru1.EnterScope(nil)
	c.Assert(err, gc.IsNil)
	logging0, err := logging.Unit("logging/0")
	c.Assert(err, gc.IsNil)
	wc.AssertChange("logging/0")
	wc.AssertNoChange()

	// Change the subordinate; no change.
	err = logging0.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Make the Dying unit Dead; change detected.
	err = mysql0.EnsureDead()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/0")
	wc.AssertNoChange()

	// Stop watcher; check Changes chan closed.
	testing.AssertStop(c, w)
	wc.AssertClosed()

	// Start a fresh watcher; check all units reported.
	w = s.machine.WatchUnits()
	defer testing.AssertStop(c, w)
	wc = testing.NewStringsWatcherC(c, s.State, w)
	wc.AssertChange("mysql/0", "mysql/1", "logging/0")
	wc.AssertNoChange()

	// Remove the Dead unit; no change.
	err = mysql0.Remove()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Destroy the subordinate; change detected.
	err = logging0.Destroy()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("logging/0")
	wc.AssertNoChange()

	// Unassign the principal; check subordinate departure also reported.
	err = mysql1.UnassignFromMachine()
	c.Assert(err, gc.IsNil)
	wc.AssertChange("mysql/1", "logging/0")
	wc.AssertNoChange()
}

func (s *MachineSuite) TestWatchUnitsDiesOnStateClose(c *gc.C) {
	testWatcherDiesWhenStateCloses(c, func(c *gc.C, st *state.State) waiter {
		m, err := st.Machine(s.machine.Id())
		c.Assert(err, gc.IsNil)
		w := m.WatchUnits()
		<-w.Changes()
		return w
	})
}

func (s *MachineSuite) TestAnnotatorForMachine(c *gc.C) {
	testAnnotator(c, func() (state.Annotator, error) {
		return s.State.Machine(s.machine.Id())
	})
}

func (s *MachineSuite) TestAnnotationRemovalForMachine(c *gc.C) {
	annotations := map[string]string{"mykey": "myvalue"}
	err := s.machine.SetAnnotations(annotations)
	c.Assert(err, gc.IsNil)
	err = s.machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = s.machine.Remove()
	c.Assert(err, gc.IsNil)
	ann, err := s.machine.Annotations()
	c.Assert(err, gc.IsNil)
	c.Assert(ann, gc.DeepEquals, make(map[string]string))
}

func (s *MachineSuite) TestConstraintsFromEnvironment(c *gc.C) {
	econs1 := constraints.MustParse("mem=1G")
	econs2 := constraints.MustParse("mem=2G")

	// A newly-created machine gets a copy of the environment constraints.
	err := s.State.SetEnvironConstraints(econs1)
	c.Assert(err, gc.IsNil)
	machine1, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	mcons1, err := machine1.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(mcons1, gc.DeepEquals, econs1)

	// Change environment constraints and add a new machine.
	err = s.State.SetEnvironConstraints(econs2)
	c.Assert(err, gc.IsNil)
	machine2, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	mcons2, err := machine2.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(mcons2, gc.DeepEquals, econs2)

	// Check the original machine has its original constraints.
	mcons1, err = machine1.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(mcons1, gc.DeepEquals, econs1)
}

func (s *MachineSuite) TestSetConstraints(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)

	// Constraints can be set...
	cons1 := constraints.MustParse("mem=1G")
	err = machine.SetConstraints(cons1)
	c.Assert(err, gc.IsNil)
	mcons, err := machine.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(mcons, gc.DeepEquals, cons1)

	// ...until the machine is provisioned, at which point they stick.
	err = machine.SetProvisioned("i-mstuck", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	cons2 := constraints.MustParse("mem=2G")
	err = machine.SetConstraints(cons2)
	c.Assert(err, gc.ErrorMatches, "cannot set constraints: machine is already provisioned")

	// Check the failed set had no effect.
	mcons, err = machine.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(mcons, gc.DeepEquals, cons1)
}

func (s *MachineSuite) TestSetAmbiguousConstraints(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	cons := constraints.MustParse("mem=4G instance-type=foo")
	err = machine.SetConstraints(cons)
	c.Assert(err, gc.ErrorMatches, `cannot set constraints: ambiguous constraints: "instance-type" overlaps with "mem"`)
}

func (s *MachineSuite) TestSetUnsupportedConstraintsWarning(c *gc.C) {
	defer loggo.ResetWriters()
	logger := loggo.GetLogger("test")
	logger.SetLogLevel(loggo.DEBUG)
	var tw loggo.TestWriter
	c.Assert(loggo.RegisterWriter("constraints-tester", &tw, loggo.DEBUG), gc.IsNil)

	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	cons := constraints.MustParse("mem=4G cpu-power=10")
	err = machine.SetConstraints(cons)
	c.Assert(err, gc.IsNil)
	c.Assert(tw.Log(), jc.LogMatches, jc.SimpleMessages{{
		loggo.WARNING,
		`setting constraints on machine "2": unsupported constraints: cpu-power`},
	})
	mcons, err := machine.Constraints()
	c.Assert(err, gc.IsNil)
	c.Assert(mcons, gc.DeepEquals, cons)
}

func (s *MachineSuite) TestConstraintsLifecycle(c *gc.C) {
	cons := constraints.MustParse("mem=1G")
	cannotSet := `cannot set constraints: not found or not alive`
	testWhenDying(c, s.machine, cannotSet, cannotSet, func() error {
		err := s.machine.SetConstraints(cons)
		mcons, err1 := s.machine.Constraints()
		c.Assert(err1, gc.IsNil)
		c.Assert(&mcons, jc.Satisfies, constraints.IsEmpty)
		return err
	})

	err := s.machine.Remove()
	c.Assert(err, gc.IsNil)
	err = s.machine.SetConstraints(cons)
	c.Assert(err, gc.ErrorMatches, cannotSet)
	_, err = s.machine.Constraints()
	c.Assert(err, gc.ErrorMatches, `constraints not found`)
}

func (s *MachineSuite) TestGetSetStatusWhileAlive(c *gc.C) {
	err := s.machine.SetStatus(params.StatusError, "", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set status "error" without info`)
	err = s.machine.SetStatus(params.StatusDown, "", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set status "down"`)
	err = s.machine.SetStatus(params.Status("vliegkat"), "orville", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set invalid status "vliegkat"`)

	status, info, data, err := s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusPending)
	c.Assert(info, gc.Equals, "")
	c.Assert(data, gc.HasLen, 0)

	err = s.machine.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	status, info, data, err = s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusStarted)
	c.Assert(info, gc.Equals, "")
	c.Assert(data, gc.HasLen, 0)

	err = s.machine.SetStatus(params.StatusError, "provisioning failed", params.StatusData{
		"foo": "bar",
	})
	c.Assert(err, gc.IsNil)
	status, info, data, err = s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusError)
	c.Assert(info, gc.Equals, "provisioning failed")
	c.Assert(data, gc.DeepEquals, params.StatusData{
		"foo": "bar",
	})
}

func (s *MachineSuite) TestSetStatusPending(c *gc.C) {
	err := s.machine.SetStatus(params.StatusPending, "", nil)
	c.Assert(err, gc.IsNil)
	// Cannot set status to pending once a machine is provisioned.
	err = s.machine.SetProvisioned("umbrella/0", "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	err = s.machine.SetStatus(params.StatusPending, "", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set status "pending"`)
}

func (s *MachineSuite) TestGetSetStatusWhileNotAlive(c *gc.C) {
	// When Dying set/get should work.
	err := s.machine.Destroy()
	c.Assert(err, gc.IsNil)
	err = s.machine.SetStatus(params.StatusStopped, "", nil)
	c.Assert(err, gc.IsNil)
	status, info, data, err := s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusStopped)
	c.Assert(info, gc.Equals, "")
	c.Assert(data, gc.HasLen, 0)

	// When Dead set should fail, but get will work.
	err = s.machine.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = s.machine.SetStatus(params.StatusStarted, "not really", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set status of machine "1": not found or not alive`)
	status, info, data, err = s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusStopped)
	c.Assert(info, gc.Equals, "")
	c.Assert(data, gc.HasLen, 0)

	err = s.machine.Remove()
	c.Assert(err, gc.IsNil)
	err = s.machine.SetStatus(params.StatusStarted, "not really", nil)
	c.Assert(err, gc.ErrorMatches, `cannot set status of machine "1": not found or not alive`)
	_, _, _, err = s.machine.Status()
	c.Assert(err, gc.ErrorMatches, "status not found")
}

func (s *MachineSuite) TestGetSetStatusDataStandard(c *gc.C) {
	err := s.machine.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	_, _, _, err = s.machine.Status()
	c.Assert(err, gc.IsNil)

	// Regular status setting with data.
	err = s.machine.SetStatus(params.StatusError, "provisioning failed", params.StatusData{
		"1st-key": "one",
		"2nd-key": 2,
		"3rd-key": true,
	})
	c.Assert(err, gc.IsNil)
	status, info, data, err := s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusError)
	c.Assert(info, gc.Equals, "provisioning failed")
	c.Assert(data, gc.DeepEquals, params.StatusData{
		"1st-key": "one",
		"2nd-key": 2,
		"3rd-key": true,
	})
}

func (s *MachineSuite) TestGetSetStatusDataMongo(c *gc.C) {
	err := s.machine.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	_, _, _, err = s.machine.Status()
	c.Assert(err, gc.IsNil)

	// Status setting with MongoDB special values.
	err = s.machine.SetStatus(params.StatusError, "mongo", params.StatusData{
		`{name: "Joe"}`: "$where",
		"eval":          `eval(function(foo) { return foo; }, "bar")`,
		"mapReduce":     "mapReduce",
		"group":         "group",
	})
	c.Assert(err, gc.IsNil)
	status, info, data, err := s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusError)
	c.Assert(info, gc.Equals, "mongo")
	c.Assert(data, gc.DeepEquals, params.StatusData{
		`{name: "Joe"}`: "$where",
		"eval":          `eval(function(foo) { return foo; }, "bar")`,
		"mapReduce":     "mapReduce",
		"group":         "group",
	})
}

func (s *MachineSuite) TestGetSetStatusDataChange(c *gc.C) {
	err := s.machine.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)
	_, _, _, err = s.machine.Status()
	c.Assert(err, gc.IsNil)

	// Status setting and changing data afterwards.
	data := params.StatusData{
		"1st-key": "one",
		"2nd-key": 2,
		"3rd-key": true,
	}
	err = s.machine.SetStatus(params.StatusError, "provisioning failed", data)
	c.Assert(err, gc.IsNil)
	data["4th-key"] = 4.0

	status, info, data, err := s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusError)
	c.Assert(info, gc.Equals, "provisioning failed")
	c.Assert(data, gc.DeepEquals, params.StatusData{
		"1st-key": "one",
		"2nd-key": 2,
		"3rd-key": true,
	})

	// Set status data to nil, so an empty map will be returned.
	err = s.machine.SetStatus(params.StatusStarted, "", nil)
	c.Assert(err, gc.IsNil)

	status, info, data, err = s.machine.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusStarted)
	c.Assert(info, gc.Equals, "")
	c.Assert(data, gc.HasLen, 0)
}

func (s *MachineSuite) TestSetAddresses(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), gc.HasLen, 0)

	addresses := []network.Address{
		network.NewAddress("127.0.0.1", network.ScopeUnknown),
		network.NewAddress("8.8.8.8", network.ScopeUnknown),
	}
	err = machine.SetAddresses(addresses...)
	c.Assert(err, gc.IsNil)
	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), jc.SameContents, addresses)
}

func (s *MachineSuite) TestSetMachineAddresses(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), gc.HasLen, 0)

	addresses := []network.Address{
		network.NewAddress("127.0.0.1", network.ScopeUnknown),
		network.NewAddress("8.8.8.8", network.ScopeUnknown),
	}
	err = machine.SetMachineAddresses(addresses...)
	c.Assert(err, gc.IsNil)
	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	c.Assert(machine.MachineAddresses(), gc.DeepEquals, addresses)
}

func (s *MachineSuite) TestMergedAddresses(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), gc.HasLen, 0)

	addresses := network.NewAddresses(
		"127.0.0.1", "8.8.8.8", "fc00::1", "::1", "", "example.org",
	)
	err = machine.SetAddresses(addresses...)
	c.Assert(err, gc.IsNil)

	machineAddresses := network.NewAddresses(
		"127.0.0.1", "localhost", "192.168.0.1", "fe80::1", "::1", "fd00::1",
	)
	err = machine.SetMachineAddresses(machineAddresses...)
	c.Assert(err, gc.IsNil)
	err = machine.Refresh()
	c.Assert(err, gc.IsNil)

	c.Assert(machine.Addresses(), jc.DeepEquals, network.NewAddresses(
		"example.org",
		"8.8.8.8",
		"127.0.0.1",
		"fc00::1",
		"::1",
		"192.168.0.1",
		"localhost",
		"fe80::1",
		"fd00::1",
	))

	// Now simulate prefer-ipv6: true
	c.Assert(
		s.State.UpdateEnvironConfig(
			map[string]interface{}{"prefer-ipv6": true},
			nil, nil,
		),
		gc.IsNil,
	)

	err = machine.SetAddresses(addresses...)
	c.Assert(err, gc.IsNil)
	err = machine.SetMachineAddresses(machineAddresses...)
	c.Assert(err, gc.IsNil)
	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), jc.DeepEquals, network.NewAddresses(
		"::1",
		"fc00::1",
		"example.org",
		"8.8.8.8",
		"127.0.0.1",
		"fd00::1",
		"fe80::1",
		"localhost",
		"192.168.0.1",
	))
}

func (s *MachineSuite) TestSetAddressesConcurrentChangeDifferent(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), gc.HasLen, 0)

	addr0 := network.NewAddress("127.0.0.1", network.ScopeUnknown)
	addr1 := network.NewAddress("8.8.8.8", network.ScopeUnknown)

	defer state.SetBeforeHooks(c, s.State, func() {
		machine, err := s.State.Machine(machine.Id())
		c.Assert(err, gc.IsNil)
		err = machine.SetAddresses(addr1, addr0)
		c.Assert(err, gc.IsNil)
	}).Check()

	err = machine.SetAddresses(addr0, addr1)
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), jc.SameContents, []network.Address{addr0, addr1})
}

func (s *MachineSuite) TestSetAddressesConcurrentChangeEqual(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(machine.Addresses(), gc.HasLen, 0)
	revno0, err := state.TxnRevno(s.State, "machines", machine.Id())
	c.Assert(err, gc.IsNil)

	addr0 := network.NewAddress("127.0.0.1", network.ScopeUnknown)
	addr1 := network.NewAddress("8.8.8.8", network.ScopeUnknown)

	var revno1 int64
	defer state.SetBeforeHooks(c, s.State, func() {
		machine, err := s.State.Machine(machine.Id())
		c.Assert(err, gc.IsNil)
		err = machine.SetAddresses(addr0, addr1)
		c.Assert(err, gc.IsNil)
		revno1, err = state.TxnRevno(s.State, "machines", machine.Id())
		c.Assert(err, gc.IsNil)
		c.Assert(revno1, gc.Equals, revno0+1)
	}).Check()

	err = machine.SetAddresses(addr0, addr1)
	c.Assert(err, gc.IsNil)

	// Doc should not have been updated, but Machine object's view should be.
	revno2, err := state.TxnRevno(s.State, "machines", machine.Id())
	c.Assert(err, gc.IsNil)
	c.Assert(revno2, gc.Equals, revno1)
	c.Assert(machine.Addresses(), jc.SameContents, []network.Address{addr0, addr1})
}

func (s *MachineSuite) addMachineWithSupportedContainer(c *gc.C, container instance.ContainerType) *state.Machine {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	containers := []instance.ContainerType{container}
	err = machine.SetSupportedContainers(containers)
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, containers)
	return machine
}

// assertSupportedContainers checks the document in memory has the specified
// containers and then reloads the document from the database to assert saved
// values match also.
func assertSupportedContainers(c *gc.C, machine *state.Machine, containers []instance.ContainerType) {
	supportedContainers, known := machine.SupportedContainers()
	c.Assert(known, jc.IsTrue)
	c.Assert(supportedContainers, gc.DeepEquals, containers)
	// Reload so we can check the saved values.
	err := machine.Refresh()
	c.Assert(err, gc.IsNil)
	supportedContainers, known = machine.SupportedContainers()
	c.Assert(known, jc.IsTrue)
	c.Assert(supportedContainers, gc.DeepEquals, containers)
}

func assertSupportedContainersUnknown(c *gc.C, machine *state.Machine) {
	containers, known := machine.SupportedContainers()
	c.Assert(known, jc.IsFalse)
	c.Assert(containers, gc.HasLen, 0)
}

func (s *MachineSuite) TestSupportedContainersInitiallyUnknown(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	assertSupportedContainersUnknown(c, machine)
}

func (s *MachineSuite) TestSupportsNoContainers(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)

	err = machine.SupportsNoContainers()
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{})
}

func (s *MachineSuite) TestSetSupportedContainerTypeNoneIsError(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)

	err = machine.SetSupportedContainers([]instance.ContainerType{instance.LXC, instance.NONE})
	c.Assert(err, gc.ErrorMatches, `"none" is not a valid container type`)
	assertSupportedContainersUnknown(c, machine)
	err = machine.Refresh()
	c.Assert(err, gc.IsNil)
	assertSupportedContainersUnknown(c, machine)
}

func (s *MachineSuite) TestSupportsNoContainersOverwritesExisting(c *gc.C) {
	machine := s.addMachineWithSupportedContainer(c, instance.LXC)

	err := machine.SupportsNoContainers()
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{})
}

func (s *MachineSuite) TestSetSupportedContainersSingle(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)

	err = machine.SetSupportedContainers([]instance.ContainerType{instance.LXC})
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{instance.LXC})
}

func (s *MachineSuite) TestSetSupportedContainersSame(c *gc.C) {
	machine := s.addMachineWithSupportedContainer(c, instance.LXC)

	err := machine.SetSupportedContainers([]instance.ContainerType{instance.LXC})
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{instance.LXC})
}

func (s *MachineSuite) TestSetSupportedContainersNew(c *gc.C) {
	machine := s.addMachineWithSupportedContainer(c, instance.LXC)

	err := machine.SetSupportedContainers([]instance.ContainerType{instance.LXC, instance.KVM})
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{instance.LXC, instance.KVM})
}

func (s *MachineSuite) TestSetSupportedContainersMultipeNew(c *gc.C) {
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)

	err = machine.SetSupportedContainers([]instance.ContainerType{instance.LXC, instance.KVM})
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{instance.LXC, instance.KVM})
}

func (s *MachineSuite) TestSetSupportedContainersMultipleExisting(c *gc.C) {
	machine := s.addMachineWithSupportedContainer(c, instance.LXC)

	err := machine.SetSupportedContainers([]instance.ContainerType{instance.LXC, instance.KVM})
	c.Assert(err, gc.IsNil)
	assertSupportedContainers(c, machine, []instance.ContainerType{instance.LXC, instance.KVM})
}

func (s *MachineSuite) TestSetSupportedContainersSetsUnknownToError(c *gc.C) {
	// Create a machine and add lxc and kvm containers prior to calling SetSupportedContainers
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	template := state.MachineTemplate{
		Series: "quantal",
		Jobs:   []state.MachineJob{state.JobHostUnits},
	}
	container, err := s.State.AddMachineInsideMachine(template, machine.Id(), instance.LXC)
	c.Assert(err, gc.IsNil)
	supportedContainer, err := s.State.AddMachineInsideMachine(template, machine.Id(), instance.KVM)
	c.Assert(err, gc.IsNil)
	err = machine.SetSupportedContainers([]instance.ContainerType{instance.KVM})
	c.Assert(err, gc.IsNil)

	// A supported (kvm) container will have a pending status.
	err = supportedContainer.Refresh()
	c.Assert(err, gc.IsNil)
	status, info, data, err := supportedContainer.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusPending)

	// An unsupported (lxc) container will have an error status.
	err = container.Refresh()
	c.Assert(err, gc.IsNil)
	status, info, data, err = container.Status()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, params.StatusError)
	c.Assert(info, gc.Equals, "unsupported container")
	c.Assert(data, gc.DeepEquals, params.StatusData{"type": "lxc"})
}

func (s *MachineSuite) TestSupportsNoContainersSetsAllToError(c *gc.C) {
	// Create a machine and add all container types prior to calling SupportsNoContainers
	machine, err := s.State.AddMachine("quantal", state.JobHostUnits)
	var containers []*state.Machine
	template := state.MachineTemplate{
		Series: "quantal",
		Jobs:   []state.MachineJob{state.JobHostUnits},
	}
	for _, containerType := range instance.ContainerTypes {
		container, err := s.State.AddMachineInsideMachine(template, machine.Id(), containerType)
		c.Assert(err, gc.IsNil)
		containers = append(containers, container)
	}

	err = machine.SupportsNoContainers()
	c.Assert(err, gc.IsNil)

	// All containers should be in error state.
	for _, container := range containers {
		err = container.Refresh()
		c.Assert(err, gc.IsNil)
		status, info, data, err := container.Status()
		c.Assert(err, gc.IsNil)
		c.Assert(status, gc.Equals, params.StatusError)
		c.Assert(info, gc.Equals, "unsupported container")
		containerType := state.ContainerTypeFromId(container.Id())
		c.Assert(data, gc.DeepEquals, params.StatusData{"type": string(containerType)})
	}
}

func (s *MachineSuite) TestWatchInterfaces(c *gc.C) {
	// Provision the machine.
	networks := []state.NetworkInfo{{
		Name:       "net1",
		ProviderId: "net1",
		CIDR:       "0.1.2.0/24",
		VLANTag:    0,
	}, {
		Name:       "vlan42",
		ProviderId: "vlan42",
		CIDR:       "0.2.2.0/24",
		VLANTag:    42,
	}}
	interfaces := []state.NetworkInterfaceInfo{{
		MACAddress:    "aa:bb:cc:dd:ee:f0",
		InterfaceName: "eth0",
		NetworkName:   "net1",
		IsVirtual:     false,
	}, {
		MACAddress:    "aa:bb:cc:dd:ee:f1",
		InterfaceName: "eth1",
		NetworkName:   "net1",
		IsVirtual:     false,
		Disabled:      true,
	}, {
		MACAddress:    "aa:bb:cc:dd:ee:f1",
		InterfaceName: "eth1.42",
		NetworkName:   "vlan42",
		IsVirtual:     true,
	}}
	err := s.machine.SetInstanceInfo("umbrella/0", "fake_nonce", nil, networks, interfaces)
	c.Assert(err, gc.IsNil)

	// Read dynamically generated document Ids.
	ifaces, err := s.machine.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 3)

	// Start network interface watcher.
	w := s.machine.WatchInterfaces()
	defer testing.AssertStop(c, w)
	wc := testing.NewNotifyWatcherC(c, s.State, w)
	wc.AssertOneChange()

	// Disable the first interface.
	err = ifaces[0].Disable()
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Disable the first interface again, should not report.
	err = ifaces[0].Disable()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Enable the second interface, should report, because it was initially disabled.
	err = ifaces[1].Enable()
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Disable two interfaces at once, check that both are reported.
	err = ifaces[1].Disable()
	c.Assert(err, gc.IsNil)
	err = ifaces[2].Disable()
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Enable the first interface.
	err = ifaces[0].Enable()
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Enable the first interface again, should not report.
	err = ifaces[0].Enable()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Remove the network interface.
	err = ifaces[0].Remove()
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Add the new interface.
	_, _ = addNetworkAndInterface(
		c, s.State, s.machine,
		"net2", "net2", "0.5.2.0/24", 0, false,
		"aa:bb:cc:dd:ee:f2", "eth2")
	wc.AssertOneChange()

	// Provision another machine, should not report
	machine2, err := s.State.AddMachine("quantal", state.JobHostUnits)
	c.Assert(err, gc.IsNil)
	interfaces2 := []state.NetworkInterfaceInfo{{
		MACAddress:    "aa:bb:cc:dd:ee:e0",
		InterfaceName: "eth0",
		NetworkName:   "net1",
		IsVirtual:     false,
	}, {
		MACAddress:    "aa:bb:cc:dd:ee:e1",
		InterfaceName: "eth1",
		NetworkName:   "net1",
		IsVirtual:     false,
	}, {
		MACAddress:    "aa:bb:cc:dd:ee:e1",
		InterfaceName: "eth1.42",
		NetworkName:   "vlan42",
		IsVirtual:     true,
	}}
	err = machine2.SetInstanceInfo("m-too", "fake_nonce", nil, networks, interfaces2)
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces, gc.HasLen, 3)
	wc.AssertNoChange()

	// Read dynamically generated document Ids.
	ifaces2, err := machine2.NetworkInterfaces()
	c.Assert(err, gc.IsNil)
	c.Assert(ifaces2, gc.HasLen, 3)

	// Disable the first interface on the second machine, should not report.
	err = ifaces2[0].Disable()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Remove the network interface on the second machine, should not report.
	err = ifaces2[0].Remove()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// Stop watcher; check Changes chan closed.
	testing.AssertStop(c, w)
	wc.AssertClosed()
}

func (s *MachineSuite) TestWatchInterfacesDiesOnStateClose(c *gc.C) {
	testWatcherDiesWhenStateCloses(c, func(c *gc.C, st *state.State) waiter {
		m, err := st.Machine(s.machine.Id())
		c.Assert(err, gc.IsNil)
		w := m.WatchInterfaces()
		<-w.Changes()
		return w
	})
}
