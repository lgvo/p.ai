// Package errtext holds the sentences P's clients show for control-API
// errors. The daemon answers with a kind and a reason code and no text
// (docs/control-api.md#errors); the TUI and the CLI write their sentence
// from those codes here, one per kind and one per reason.
package errtext

import (
	"errors"

	"github.com/lgvo/p.ai/internal/api"
)

// Sentence is the sentence a client shows for err. An *api.Error gets its
// reason's sentence, or its kind's when the reason is unknown or absent; any
// other error keeps its own text.
func Sentence(err error) string {
	var e *api.Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	if s, ok := reasonSentences[e.Kind()][e.Reason()]; ok {
		return s
	}
	if e.Kind() == api.KindInvalid && e.Data != nil && e.Data.Field != "" {
		return "The request's " + e.Data.Field + " is not valid."
	}
	if s, ok := kindSentences[e.Kind()]; ok {
		return s
	}
	return "The daemon refused the request (" + e.Message + ")."
}

// kindSentences says what each kind means and what to do next.
var kindSentences = map[api.ErrorKind]string{
	api.KindParseError:           "The daemon could not read the request.",
	api.KindInvalidRequest:       "The daemon refused a malformed request.",
	api.KindMethodNotFound:       "The daemon does not offer that request; use a matching version of P.",
	api.KindInvalid:              "The request is not valid.",
	api.KindInternal:             "P failed unexpectedly; the daemon's log has the cause.",
	api.KindCancelled:            "The request was cancelled.",
	api.KindUnsupportedVersion:   "This client and the daemon speak different protocols; use a matching version of P.",
	api.KindRetry:                "P is busy with this right now; try again in a moment.",
	api.KindConflict:             "The current state does not allow this.",
	api.KindCapacity:             "No room is left for this; discard or delete a session first.",
	api.KindPreconditionFailed:   "What you reviewed has changed; review it again.",
	api.KindPreconditionRequired: "This change needs a reviewed plan; review it first.",
	api.KindNotFound:             "It no longer exists.",
	api.KindNotConfigured:        "This P instance is not configured for that.",
	api.KindFailed:               "A system P depends on failed; check it and try again.",
}

// reasonSentences refines a kind's sentence for a reason.
var reasonSentences = map[api.ErrorKind]map[string]string{
	api.KindInvalid: {
		api.ReasonKeyReused: "That request key was already used for a different request; use a new key.",
	},
	api.KindRetry: {
		api.ReasonSessionHeld:     "Another action is using this session; try again when it finishes.",
		api.ReasonTooManyRequests: "Too many requests at once; try again.",
		api.ReasonDeadline:        "The daemon took too long to answer; try again.",
		api.ReasonTooManyPreviews: "Too many reviews are open; try again in a minute.",
		api.ReasonProjectHeld:     "Another action is using this project; try again when it finishes.",
		api.ReasonRefsMoved:       "P's branches moved while P read them; try again.",
	},
	api.KindConflict: {
		api.ReasonAttachmentActive:       "Detach every terminal from this session first.",
		api.ReasonSessionNotEstablished:  "This session is still being created or removed.",
		api.ReasonSessionNotStopped:      "Stop this session first.",
		api.ReasonSessionNotRunning:      "Start this session first.",
		api.ReasonRuntimeMissing:         "This session's container is missing; discard or delete the session.",
		api.ReasonOperationBlocked:       "A blocked operation holds this; retry it or deal with it first.",
		api.ReasonOperationSuperseded:    "That operation was replaced and cannot run again.",
		api.ReasonOperationUnfinished:    "An unfinished change still holds this project; finish it first.",
		api.ReasonRuntimeUnsettled:       "This session's container is neither running nor stopped; wait for it to settle.",
		api.ReasonRuntimePresent:         "A container of this session exists where none was expected; review again.",
		api.ReasonOperationNotRetryable:  "That operation cannot be retried.",
		api.ReasonPolicyInvalid:          "The project's policy is not configured or no longer holds; fix the host configuration.",
		api.ReasonPrincipalMissing:       "This session's Git identity is gone; discard the session and create a new one.",
		api.ReasonBranchExists:           "That branch name is taken; choose another.",
		api.ReasonBranchAssigned:         "A session already uses that branch.",
		api.ReasonBranchReserved:         "Another operation is using that branch; try again when it finishes.",
		api.ReasonProjectExists:          "That project already exists.",
		api.ReasonProjectDeleting:        "This project is being deleted.",
		api.ReasonOriginNotSet:           "This project has no origin; set one first.",
		api.ReasonCleanupActive:          "A cleanup holds this session; wait for it to finish.",
		api.ReasonBoundExceeded:          "The answer would be too large to send.",
		api.ReasonPublicEgressUnassigned: "This session has no public network address; discard it and create a new one.",
	},
	api.KindCapacity: {
		api.ReasonSessionCapacity:    "No room is left for another session; discard or delete a session. Stop keeps its slot.",
		api.ReasonAttachmentCapacity: "Too many terminals are waiting to attach; try again shortly.",
		api.ReasonPublicAddresses:    "Every public network address is in use; discard or delete a session on the public network.",
	},
	api.KindPreconditionFailed: {
		api.ReasonExpired: "The review or attachment expired or was already used; request it again.",
		api.ReasonChanged: "Something changed since you looked; review again.",
	},
	api.KindNotFound: {
		api.ReasonSession:   "That session no longer exists.",
		api.ReasonOperation: "That operation does not exist.",
		api.ReasonProject:   "That project does not exist.",
		api.ReasonBranch:    "That branch does not exist.",
		api.ReasonSource:    "That source does not exist.",
	},
	api.KindNotConfigured: {
		api.ReasonGit:           "This P instance has no Git configured.",
		api.ReasonRuntime:       "This P instance has no runtime configured.",
		api.ReasonServices:      "This runtime does not offer project services.",
		api.ReasonPublicEgress:  "This P instance has no public network configured.",
		api.ReasonProjectPolicy: "The host configuration has no policy for this project; add one.",
	},
	api.KindFailed: {
		api.ReasonRuntime:  "The runtime failed; check Incus and try again.",
		api.ReasonGit:      "P's Git repository failed; try again.",
		api.ReasonServices: "The session's user service manager refused or is unavailable; check the unit and the session's image.",
		api.ReasonOrigin:   "The origin could not be reached; check its URL, the SSH identity and access, then try again.",
		api.ReasonPlugin:   "A plugin failed; check it and try again.",
	},
}

// Operation is the sentence a client shows for an operation that stopped
// with status blocked or failed and reason: what the reason means, then the
// next step for that status. It is empty for an operation with no reason.
func Operation(status, reason string) string {
	if reason == "" {
		return ""
	}
	sentence, ok := operationSentences[reason]
	if !ok {
		sentence = "It stopped: " + reason + "."
	}
	switch status {
	case "blocked":
		return sentence + " Retry it once that is resolved."
	case "failed":
		return sentence + " It ended; start a new request."
	}
	return sentence
}

// operationSentences says what each reason an operation records means.
var operationSentences = map[string]string{
	api.ReasonInternal:            "P failed in a way it does not classify; the diagnostic has the detail.",
	api.ReasonStore:               "P's state store failed; the daemon's log has the cause.",
	api.ReasonRuntime:             "The runtime failed; check Incus.",
	api.ReasonGit:                 "P's Git repository failed.",
	api.ReasonOrigin:              "The origin could not be reached; check its URL, the SSH identity and access.",
	api.ReasonPlugin:              "A selected plugin is unavailable or changed; check the host configuration.",
	api.ReasonChanged:             "What it was confirmed with changed.",
	api.ReasonExpired:             "Its review expired.",
	api.ReasonOutcomeUnknown:      "An effect it issued may or may not have happened; P does not issue it again. Check the resource the diagnostic names.",
	api.ReasonWorkspaceMismatch:   "The session's workspace is not on its assigned branch or tracks another upstream; correct it with Git.",
	api.ReasonImageBuilderChanged: "The host's image builder configuration changed since this creation began. To use the new one, clean up this creation and create the session again; otherwise restore the configuration.",
	api.ReasonPolicyInvalid:       "The project's policy no longer holds on this host.",
	api.ReasonPublicEgress:        "This P instance has no public network configured.",
	api.ReasonRuntimeUnsettled:    "The session's container is in an unexpected state.",
	api.ReasonRuntimePresent:      "A container P does not recognize holds the session's name.",
	api.ReasonRuntimeMissing:      "The session's container is missing.",
	api.ReasonOperationUnfinished: "Another unfinished operation holds what this one needs.",
	api.ReasonRefsMoved:           "P's branches moved while it read them.",
	api.ReasonBranchExists:        "The new branch name is taken.",
	api.ReasonBranchReserved:      "Another operation reserves the branch.",
	api.ReasonPrincipalMissing:    "The session's Git identity is missing or changed.",
	api.ReasonSessionNotStopped:   "The session is not stopped.",
	api.ReasonSession:             "Its session is gone.",
	api.ReasonProject:             "Its project is gone.",
	api.ReasonBranch:              "A branch it needs is gone.",
	api.ReasonSource:              "Its source is gone.",
	api.ReasonOperation:           "An operation it depends on is gone.",
}
