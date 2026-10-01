package main

import (
	"fmt"
	"net/http"
)

func runWithdrawGateCommand(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-north", "北侧泄洪闸", 4, 0.5); err != nil {
		return err
	}
	command, err := fixtureCommand(client, "gate-north", 2.5, "预泄调度", "dispatcher-qian")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
		return err
	}

	pendingWithdraw, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "上游水位突变",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(pendingWithdraw, http.StatusConflict, "reject withdraw of pending command"); err != nil {
		return err
	}

	approved, err := fixtureClearReview(client, commandID, "safety-wu")
	if err != nil {
		return err
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}

	missingReason, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "  ",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(missingReason, http.StatusBadRequest, "reject withdraw without reason"); err != nil {
		return err
	}
	if err := requireGateState(client, "gate-north", "reserved", commandID); err != nil {
		return fmt.Errorf("failed withdraw must not release gate: %w", err)
	}
	if err := requireCommandState(client, commandID, "approved"); err != nil {
		return fmt.Errorf("failed withdraw must not change command: %w", err)
	}

	withdrawn, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "上游水位突变，条件不再满足",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(withdrawn, http.StatusOK, "withdraw approved command"); err != nil {
		return err
	}
	withdrawnCommand, err := objectField(withdrawn.Body, "command")
	if err != nil {
		return err
	}
	if err := explainWithdrawal(withdrawnCommand, "safety-wu"); err != nil {
		return err
	}
	if _, exists := withdrawnCommand["execution_token"]; exists {
		return fmt.Errorf("withdrawn command must not expose an execution token")
	}
	withdrawnGate, err := objectField(withdrawn.Body, "gate")
	if err != nil {
		return err
	}
	if err := expectIdleGate(withdrawnGate); err != nil {
		return err
	}

	reusedToken, err := client.post("/commands/"+commandID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(reusedToken, http.StatusConflict, "reject execution with withdrawn token"); err != nil {
		return err
	}

	again, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "重复撤回",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(again, http.StatusConflict, "reject repeated withdraw"); err != nil {
		return err
	}

	readBack, err := client.get("/commands/" + commandID)
	if err != nil {
		return err
	}
	if err := requireStatus(readBack, http.StatusOK, "read withdrawn command"); err != nil {
		return err
	}
	readCommand, err := objectField(readBack.Body, "command")
	if err != nil {
		return err
	}
	if err := explainWithdrawal(readCommand, "safety-wu"); err != nil {
		return fmt.Errorf("read back must explain withdrawal: %w", err)
	}
	if err := requireGateState(client, "gate-north", "idle", ""); err != nil {
		return err
	}

	successor, err := fixtureCommand(client, "gate-north", 1.5, "撤回后重新调度", "dispatcher-qian")
	if err != nil {
		return err
	}
	successorID, err := stringField(successor, "id")
	if err != nil {
		return err
	}
	successorApproved, err := fixtureClearReview(client, successorID, "safety-wu")
	if err != nil {
		return fmt.Errorf("gate released by withdrawal must accept a new clear review: %w", err)
	}
	successorToken, err := stringField(successorApproved, "execution_token")
	if err != nil {
		return err
	}

	executed, err := client.post("/commands/"+successorID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          successorToken,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed, http.StatusOK, "execute successor command"); err != nil {
		return err
	}

	terminalWithdraw, err := client.post("/commands/"+successorID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "命令已完成",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(terminalWithdraw, http.StatusConflict, "reject withdraw of completed command"); err != nil {
		return err
	}
	if err := requireCommandState(client, successorID, "completed"); err != nil {
		return fmt.Errorf("failed withdraw must not change completed command: %w", err)
	}
	if err := requireGateState(client, "gate-north", "idle", ""); err != nil {
		return fmt.Errorf("failed withdraw must not touch settled gate: %w", err)
	}

	fmt.Println("withdraw-gate-command: approved command withdrawn, gate released, token invalidated, terminal states protected")
	return nil
}

func explainWithdrawal(command map[string]any, expectedBy string) error {
	status, err := stringField(command, "status")
	if err != nil {
		return err
	}
	if status != "withdrawn" {
		return fmt.Errorf("withdrawal status: expected withdrawn, got %q", status)
	}
	withdrawnBy, err := stringField(command, "withdrawn_by")
	if err != nil {
		return err
	}
	if withdrawnBy != expectedBy {
		return fmt.Errorf("withdrawn_by: expected %q, got %q", expectedBy, withdrawnBy)
	}
	reason, err := stringField(command, "terminal_reason")
	if err != nil {
		return err
	}
	if reason == "" {
		return fmt.Errorf("withdrawn command must record a terminal reason")
	}
	return nil
}

func expectIdleGate(gate map[string]any) error {
	status, err := stringField(gate, "status")
	if err != nil {
		return err
	}
	if status != "idle" {
		return fmt.Errorf("gate status: expected idle, got %q", status)
	}
	if active, exists := gate["active_command_id"]; exists && active != "" {
		return fmt.Errorf("idle gate must not reference an active command, got %v", active)
	}
	return nil
}

func requireGateState(client *apiClient, gateID, expectedStatus, expectedActiveCommandID string) error {
	result, err := client.get("/gates/" + gateID)
	if err != nil {
		return err
	}
	if err := requireStatus(result, http.StatusOK, "read gate "+gateID); err != nil {
		return err
	}
	gate, err := objectField(result.Body, "gate")
	if err != nil {
		return err
	}
	status, err := stringField(gate, "status")
	if err != nil {
		return err
	}
	if status != expectedStatus {
		return fmt.Errorf("gate %s status: expected %q, got %q", gateID, expectedStatus, status)
	}
	active, _ := gate["active_command_id"].(string)
	if active != expectedActiveCommandID {
		return fmt.Errorf("gate %s active command: expected %q, got %q", gateID, expectedActiveCommandID, active)
	}
	return nil
}

func requireCommandState(client *apiClient, commandID, expectedStatus string) error {
	result, err := client.get("/commands/" + commandID)
	if err != nil {
		return err
	}
	if err := requireStatus(result, http.StatusOK, "read command "+commandID); err != nil {
		return err
	}
	command, err := objectField(result.Body, "command")
	if err != nil {
		return err
	}
	status, err := stringField(command, "status")
	if err != nil {
		return err
	}
	if status != expectedStatus {
		return fmt.Errorf("command %s status: expected %q, got %q", commandID, expectedStatus, status)
	}
	return nil
}
