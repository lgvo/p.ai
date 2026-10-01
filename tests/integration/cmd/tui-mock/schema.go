package main

import (
	"fmt"
	"strings"
)

// These schemas describe the requests actually issued by the production TUI.
// Reject misspelled fields so a broken client cannot pass through a lenient
// fixture. Lifecycle/backend validation remains owned by control's tests.
var fields = map[string]string{
	"system.capabilities": "", "system.health": "",
	"project.list": "after limit", "session.list": "after limit project", "operation.list": "after limit",
	"project.branches": "project after limit", "project.retained_branches": "project after limit",
	"origin.refresh": "project", "origin.sources": "project after limit",
	"project.create":  "key project url",
	"session.create":  "key project branch choice source origin_ref expected_commit_oid expected_origin_url",
	"session.inspect": "uuid", "session.start": "uuid", "session.stop": "uuid", "session.attach": "uuid",
	"session.rename":  "key uuid new_branch expected_old_tip",
	"session.discard": "key uuid confirmation_token", "session.delete": "key uuid confirmation_token",
	"workspace.loss.inspect": "key uuid", "session.removal.preview": "uuid kind loss_operation_id",
	"operation.inspect": "id", "operation.retry": "id",
	"session.services": "uuid", "session.service.action": "uuid unit action", "session.service.journal": "uuid unit",
	"attachment.claim": "token", "attachment.confirm": "operation", "attachment.ping": "",
	"mock.inspect": "", "mock.configure": "method delay_ms error services_empty",
}

func validateParams(method string, p object) error {
	names, known := fields[method]
	if !known {
		return nil
	}
	allowed := map[string]bool{"v": true}
	for _, name := range strings.Fields(names) {
		allowed[name] = true
	}
	for name, value := range p {
		if !allowed[name] {
			return fmt.Errorf("unexpected %s parameter %s", method, name)
		}
		if name == "v" || name == "limit" || name == "delay_ms" {
			if _, ok := value.(float64); !ok {
				return fmt.Errorf("%s must be numeric", name)
			}
		} else if name == "services_empty" {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be boolean", name)
			}
		} else if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", name)
		}
	}
	if strings.Contains(" "+names+" ", " key ") {
		if p["key"] == nil || p["key"] == "" {
			return fmt.Errorf("%s requires key", method)
		}
	}
	return nil
}
