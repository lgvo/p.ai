package api

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestPlanDetailsKeepEveryPreviewField compares each risky change's former
// preview, field by field as the preview methods returned it before plans,
// with its plan: every field is in the details, or moved to the envelope (the
// action, the target, allowed and refusals, a loss item), or is one the plan
// removes by design (docs/control-api.md#risky-changes), or one a later
// change added by design.
func TestPlanDetailsKeepEveryPreviewField(t *testing.T) {
	for _, change := range []struct {
		method  string
		details reflect.Type
		// preview is the former preview's fields.
		preview string
		// moved went to the envelope or a loss item; removed are gone;
		// added are new.
		moved, removed, added string
	}{
		{"session.discard and session.delete", reflect.TypeFor[RemovalDetails](),
			"kind session_uuid project branch assigned_ref assigned_tip assigned_ref_missing policy_sha256 runtime branch_loss confirmation_token expires_at",
			"kind session_uuid", "confirmation_token expires_at", ""},
		// The repository is the source driver's, named by its incarnation:
		// core never sees its path.
		{"project.delete", reflect.TypeFor[ProjectDeleteDetails](),
			"project outcome state_sha256 project_policy_sha256 external_mounts_preserved sessions attachments branch_loss images repository_identity repository_parent_identity warnings confirmation_token expires_at",
			"warnings", "confirmation_token expires_at repository_identity repository_parent_identity", "repository_incarnation"},
		{"project.retained.delete", reflect.TypeFor[RetainedDeleteDetails](),
			"project branch ref tip branch_loss confirmation_token expires_at",
			"", "confirmation_token expires_at", ""},
		{"session.create.replace", reflect.TypeFor[CreateReplaceDetails](),
			"old_uuid old_operation_id old_status old_phase old_request old_branch new_branch old_captured_oid old_evidence_sha256 old_policy_sha256 old_image_fingerprint new_request new_captured_oid new_image_fingerprint new_selection new_environment new_policy_sha256 provisional unsafe_reasons eligible confirmation_token expires_at",
			"old_uuid unsafe_reasons eligible", "confirmation_token expires_at", ""},
		{"session.create.cleanup", reflect.TypeFor[CreateCleanupDetails](),
			"uuid old_operation_id old_request old_phase old_evidence_sha256 old_request_sha256 policy_sha256 assigned_branch image_fingerprint environment_builder provisional external_mounts shared_images runtime loss_warnings eligible unsafe_reasons confirmation_token expires_at",
			"uuid loss_warnings eligible unsafe_reasons", "confirmation_token expires_at", ""},
		{"origin.publish", reflect.TypeFor[PublicationPreview](),
			"project url source_kind source source_ref source_oid destination_ref destination_oid relation workspace_status",
			"", "", ""},
	} {
		left := map[string]bool{}
		for _, f := range strings.Fields(change.preview) {
			left[f] = true
		}
		for _, f := range strings.Fields(change.moved + " " + change.removed) {
			delete(left, f)
		}
		for _, f := range strings.Fields(change.added) {
			left[f] = true
		}
		var want, got []string
		for f := range left {
			want = append(want, f)
		}
		for i := 0; i < change.details.NumField(); i++ {
			got = append(got, strings.Split(change.details.Field(i).Tag.Get("json"), ",")[0])
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s details have %v, want the preview's %v", change.method, got, want)
		}
	}
	// A removal's runtime keeps the preview's fields but its loss flags, now
	// loss and gone items; a retained branch's branch loss keeps all but the
	// origin, which deletions no longer read.
	for _, nested := range []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"runtime", reflect.TypeFor[RemovalRuntime](), "condition generation image_fingerprint incus_project incus_uuid instance_name original_status"},
		{"branch_loss", reflect.TypeFor[RemovalBranchLoss](), "assigned_ref assigned_tip commits_losing_p_reachability p_refs"},
	} {
		var got []string
		for i := 0; i < nested.typ.NumField(); i++ {
			got = append(got, strings.Split(nested.typ.Field(i).Tag.Get("json"), ",")[0])
		}
		sort.Strings(got)
		if strings.Join(got, " ") != nested.want {
			t.Errorf("%s has %v, want %s", nested.name, got, nested.want)
		}
	}
}
