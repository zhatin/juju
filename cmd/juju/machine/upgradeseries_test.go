// Copyright 2018 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package machine_test

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/golang/mock/gomock"
	"github.com/juju/cmd"
	"github.com/juju/cmd/cmdtesting"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/cmd/juju/machine"
	"github.com/juju/juju/cmd/juju/machine/mocks"
	"github.com/juju/juju/testing"
)

type UpgradeSeriesSuite struct {
	testing.BaseSuite

	prepareExpectation  *upgradeSeriesPrepareExpectation
	completeExpectation *upgradeSeriesCompleteExpectation
}

var _ = gc.Suite(&UpgradeSeriesSuite{})

func (s *UpgradeSeriesSuite) SetUpTest(c *gc.C) {
	s.BaseSuite.SetUpTest(c)
	s.prepareExpectation = &upgradeSeriesPrepareExpectation{gomock.Any(), gomock.Any(), gomock.Any()}
	s.completeExpectation = &upgradeSeriesCompleteExpectation{gomock.Any()}
}

const machineArg = "1"
const seriesArg = "xenial"

func (s *UpgradeSeriesSuite) runUpgradeSeriesCommand(c *gc.C, args ...string) error {
	_, err := s.runUpgradeSeriesCommandWithConfirmation(c, "y", args...)
	return err
}

func (s *UpgradeSeriesSuite) runUpgradeSeriesCommandWithConfirmation(c *gc.C, confirmation string, args ...string) (*cmd.Context, error) {
	var stdin, stdout, stderr bytes.Buffer
	ctx, err := cmd.DefaultContext()
	c.Assert(err, jc.ErrorIsNil)
	ctx.Stderr = &stderr
	ctx.Stdout = &stdout
	ctx.Stdin = &stdin
	stdin.WriteString(confirmation)

	mockController := gomock.NewController(c)
	defer mockController.Finish()
	mockUpgradeSeriesAPI := mocks.NewMockUpgradeMachineSeriesAPI(mockController)
	mockUpgradeSeriesAPI.EXPECT().UpgradeSeriesPrepare(s.prepareExpectation.machineArg, s.prepareExpectation.seriesArg, s.prepareExpectation.force).AnyTimes()
	mockUpgradeSeriesAPI.EXPECT().UpgradeSeriesComplete(s.completeExpectation.machineNumber).AnyTimes()
	com := machine.NewUpgradeSeriesCommandForTest(mockUpgradeSeriesAPI)

	err = cmdtesting.InitCommand(com, args)
	if err != nil {
		return nil, err
	}
	err = com.Run(ctx)
	if err != nil {
		return nil, err
	}
	if stderr.String() != "" {
		return nil, errors.New(stderr.String())
	}
	return ctx, nil
}

func (s *UpgradeSeriesSuite) TestPrepareCommand(c *gc.C) {
	s.prepareExpectation = &upgradeSeriesPrepareExpectation{machineArg, seriesArg, gomock.Eq(false)}
	err := s.runUpgradeSeriesCommand(c, machine.PrepareCommand, machineArg, seriesArg)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldAcceptForceOption(c *gc.C) {
	s.prepareExpectation = &upgradeSeriesPrepareExpectation{machineArg, seriesArg, gomock.Eq(true)}
	err := s.runUpgradeSeriesCommand(c, machine.PrepareCommand, machineArg, seriesArg, "--force")
	c.Assert(err, jc.ErrorIsNil)
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldAbortOnFailedConfirmation(c *gc.C) {
	_, err := s.runUpgradeSeriesCommandWithConfirmation(c, "n", machine.PrepareCommand, machineArg, seriesArg)
	c.Assert(err, gc.ErrorMatches, "upgrade series: aborted")
}

func (s *UpgradeSeriesSuite) TestUpgradeCommandShouldNotAcceptInvalidPrepCommands(c *gc.C) {
	invalidPrepCommand := "actuate"
	err := s.runUpgradeSeriesCommand(c, invalidPrepCommand, machineArg, seriesArg)
	c.Assert(err, gc.ErrorMatches, ".* \"actuate\" is an invalid upgrade-series command")
}

func (s *UpgradeSeriesSuite) TestUpgradeCommandShouldNotAcceptInvalidMachineArgs(c *gc.C) {
	invalidMachineArg := "machine5"
	err := s.runUpgradeSeriesCommand(c, machine.PrepareCommand, invalidMachineArg, seriesArg)
	c.Assert(err, gc.ErrorMatches, "\"machine5\" is an invalid machine name")
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldOnlyAcceptSupportedSeries(c *gc.C) {
	BadSeries := "Combative Caribou"
	err := s.runUpgradeSeriesCommand(c, machine.PrepareCommand, machineArg, BadSeries)
	c.Assert(err, gc.ErrorMatches, ".* is an unsupported series")
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldSupportSeriesRegardlessOfCase(c *gc.C) {
	capitalizedCaseXenial := "Xenial"
	err := s.runUpgradeSeriesCommand(c, machine.PrepareCommand, machineArg, capitalizedCaseXenial)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *UpgradeSeriesSuite) TestCompleteCommand(c *gc.C) {
	s.completeExpectation.machineNumber = machineArg
	err := s.runUpgradeSeriesCommand(c, machine.CompleteCommand, machineArg)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *UpgradeSeriesSuite) TestCompleteCommandDoesNotAcceptSeries(c *gc.C) {
	err := s.runUpgradeSeriesCommand(c, machine.CompleteCommand, machineArg, seriesArg)
	c.Assert(err, gc.ErrorMatches, "wrong number of arguments")
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldAcceptAgree(c *gc.C) {
	err := s.runUpgradeSeriesCommand(c, machine.PrepareCommand, machineArg, seriesArg, "--agree")
	c.Assert(err, jc.ErrorIsNil)
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldPromptUserForConfirmation(c *gc.C) {
	ctx, err := s.runUpgradeSeriesCommandWithConfirmation(c, "y", machine.PrepareCommand, machineArg, seriesArg)
	c.Assert(err, jc.ErrorIsNil)
	confirmationMsg := fmt.Sprintf(machine.UpgradeSeriesConfirmationMsg, machineArg, seriesArg)
	c.Assert(ctx.Stdout.(*bytes.Buffer).String(), gc.Equals, confirmationMsg)
}

func (s *UpgradeSeriesSuite) TestPrepareCommandShouldAcceptAgreeAndNotPrompt(c *gc.C) {
	ctx, err := s.runUpgradeSeriesCommandWithConfirmation(c, "n", machine.PrepareCommand, machineArg, seriesArg, "--agree")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(ctx.Stdout.(*bytes.Buffer).String(), gc.Equals, ``)
}

type upgradeSeriesPrepareExpectation struct {
	machineArg, seriesArg, force interface{}
}
type upgradeSeriesCompleteExpectation struct {
	machineNumber interface{}
}
