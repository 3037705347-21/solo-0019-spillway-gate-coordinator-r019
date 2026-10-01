package main

import (
	"fmt"
	"net/http"
)

func runWithdrawGateCommand(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-mid", "中部泄洪闸", 5, 0.5); err != nil {
		return err
	}
	command, err := fixtureCommand(client, "gate-mid", 2.5, "现场条件复核后撤消", "dispatcher-feng")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
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

	// A pending command on another gate cannot be withdrawn before review.
	if _, err := fixtureGate(client, "gate-mid-2", "中部泄洪闸二号", 5, 0.5); err != nil {
		return err
	}
	pendingCommand, err := fixtureCommand(client, "gate-mid-2", 1.0, "尚未复核的命令", "dispatcher-feng")
	if err != nil {
		return err
	}
	pendingID, err := stringField(pendingCommand, "id")
	if err != nil {
		return err
	}
	notApproved, err := client.post("/commands/"+pendingID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "提前撤回尝试",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(notApproved, http.StatusConflict, "reject withdrawal of pending command"); err != nil {
		return err
	}
	if err := requireErrorCode(notApproved, "command_not_approved"); err != nil {
		return err
	}

	// An invalid request body must not release the gate, change the command or
	// invalidate the existing token.
	missingActor, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"reason": "缺少撤回人",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(missingActor, http.StatusBadRequest, "reject withdrawal without actor"); err != nil {
		return err
	}

	withdrawn, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "现场水流快速变化，撤回尚未执行的命令",
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
	status, err := stringField(withdrawnCommand, "status")
	if err != nil {
		return err
	}
	if status != "withdrawn" {
		return fmt.Errorf("withdrawn command status: expected withdrawn, got %q", status)
	}
	if actor, err := stringField(withdrawnCommand, "withdrawn_by"); err != nil || actor != "safety-wu" {
		return fmt.Errorf("withdrawn command actor mismatch: %v %v", actor, err)
	}
	if reason, err := stringField(withdrawnCommand, "terminal_reason"); err != nil || reason == "" {
		return fmt.Errorf("withdrawn command must explain the withdrawal: %v %v", reason, err)
	}
	if value, exists := withdrawnCommand["execution_token"]; exists && value != "" {
		return fmt.Errorf("withdrawn response must not expose the old token, got %v", value)
	}
	if interlock, err := objectField(withdrawnCommand, "interlock"); err == nil {
		if verdict, err := stringField(interlock, "verdict"); err != nil || verdict != "clear" {
			return fmt.Errorf("original clear interlock must remain readable: %v %v", verdict, err)
		}
	} else {
		return fmt.Errorf("withdrawn command must retain the interlock decision: %w", err)
	}

	// The gate reservation is released and the gate can be re-reviewed safely.
	withdrawnGate, err := objectField(withdrawn.Body, "gate")
	if err != nil {
		return err
	}
	gateStatus, err := stringField(withdrawnGate, "status")
	if err != nil {
		return err
	}
	if gateStatus != "idle" {
		return fmt.Errorf("gate status after withdrawal: expected idle, got %q", gateStatus)
	}
	if value, exists := withdrawnGate["active_command_id"]; exists && value != "" {
		return fmt.Errorf("gate must release the active command reference, got %v", value)
	}

	// The original token is immediately invalid and cannot start movement.
	staleExecution, err := client.post("/commands/"+commandID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(staleExecution, http.StatusConflict, "reject execution with withdrawn token"); err != nil {
		return err
	}

	// Withdrawal is terminal: repeating it must fail without changing anything.
	secondWithdraw, err := client.post("/commands/"+commandID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "重复撤回",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(secondWithdraw, http.StatusConflict, "reject repeated withdrawal"); err != nil {
		return err
	}

	// A later read still explains the withdrawal and the released gate.
	readCommand, err := client.get("/commands/" + commandID)
	if err != nil {
		return err
	}
	if err := requireStatus(readCommand, http.StatusOK, "read withdrawn command"); err != nil {
		return err
	}
	commandObject, err := objectField(readCommand.Body, "command")
	if err != nil {
		return err
	}
	if status, err := stringField(commandObject, "status"); err != nil || status != "withdrawn" {
		return fmt.Errorf("stored command status: expected withdrawn, got %q %v", status, err)
	}
	if actor, err := stringField(commandObject, "withdrawn_by"); err != nil || actor != "safety-wu" {
		return fmt.Errorf("stored withdrawn actor mismatch: %q %v", actor, err)
	}
	if reason, err := stringField(commandObject, "terminal_reason"); err != nil || reason == "" {
		return fmt.Errorf("stored command must explain the withdrawal")
	}
	if value, exists := commandObject["execution_token"]; exists && value != "" {
		return fmt.Errorf("stored withdrawn command must not retain a reusable token")
	}

	readGate, err := client.get("/gates/gate-mid")
	if err != nil {
		return err
	}
	if err := requireStatus(readGate, http.StatusOK, "read released gate"); err != nil {
		return err
	}
	gateObject, err := objectField(readGate.Body, "gate")
	if err != nil {
		return err
	}
	if status, err := stringField(gateObject, "status"); err != nil || status != "idle" {
		return fmt.Errorf("stored gate status: expected idle, got %q %v", status, err)
	}
	if value, exists := gateObject["active_command_id"]; exists && value != "" {
		return fmt.Errorf("stored gate must not reference the withdrawn command")
	}

	// After release another command on the same gate can pass safety review and
	// execute, proving other commands can be safely re-checked.
	nextCommand, err := fixtureCommand(client, "gate-mid", 1.5, "撤回后的新命令", "dispatcher-feng")
	if err != nil {
		return err
	}
	nextID, err := stringField(nextCommand, "id")
	if err != nil {
		return err
	}
	nextApproved, err := fixtureClearReview(client, nextID, "safety-he")
	if err != nil {
		return err
	}
	nextToken, err := stringField(nextApproved, "execution_token")
	if err != nil {
		return err
	}
	nextExecution, err := client.post("/commands/"+nextID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          nextToken,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(nextExecution, http.StatusOK, "execute command after gate release"); err != nil {
		return err
	}

	// An already-terminal command (completed) cannot be withdrawn.
	completedWithdraw, err := client.post("/commands/"+nextID+"/withdraw", map[string]any{
		"withdrawn_by": "safety-wu",
		"reason":       "终态撤回尝试",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(completedWithdraw, http.StatusConflict, "reject withdrawal of completed command"); err != nil {
		return err
	}

	fmt.Println("withdraw-gate-command: approved command withdrawn, token invalidated and gate released")
	return nil
}
