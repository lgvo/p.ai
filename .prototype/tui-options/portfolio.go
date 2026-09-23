package main

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Each project has its own work history; only the developer's current projects
// have running sessions. Explicit branch names keep searches recognizable.
var portfolioProjects = []struct {
	name, sessions, branches string
}{
	{"forge", "feat/device-login fix/token-refresh docs/plugin-guide test/auth-regressions chore/drop-legacy-tokens", "fix/logout-redirect feat/passkey-login docs/auth-scopes test/session-expiry"},
	{"orbit", "fix/cache-race feat/job-retries perf/queue-batching fix/worker-shutdown chore/redis-upgrade", "feat/dead-letter-queue fix/duplicate-delivery test/retry-backoff docs/worker-setup"},
	{"p.ai", "feat/session-creation fix/project-selector docs/policy-grants feat/service-journal refactor/session-layout", "feat/policy-preview fix/branch-validation test/boot-failure docs/session-recovery"},
	{"atlas", "feat/offline-maps fix/tile-seams perf/route-search test/geocoding docs/map-providers", "feat/elevation-profile fix/coordinate-rounding test/route-detours docs/tile-cache"},
	{"beacon", "feat/alert-routing fix/silence-expiry perf/metric-ingest test/pager-fallback docs/escalations", "feat/on-call-calendar fix/alert-deduplication test/notification-delay docs/webhook-setup"},
	{"cedar", "feat/invoice-pdf fix/tax-rounding test/refund-totals chore/currency-table docs/accounting-export", "feat/credit-notes fix/overdue-reminders test/partial-payments docs/tax-rules"},
	{"delta", "feat/schema-diff fix/null-backfill perf/bulk-copy test/rollback docs/migration-runbook", "feat/dry-run-plan fix/foreign-key-order test/locked-tables docs/cutover"},
	{"ember", "feat/template-preview fix/css-inlining test/unsubscribe chore/smtp-client docs/delivery-hooks", "feat/bounce-handling fix/reply-address test/unicode-subjects docs/sender-domains"},
	{"flux", "feat/event-replay fix/offset-checkpoint perf/consumer-lag test/partition-rebalance docs/stream-contracts", "feat/schema-registry fix/poison-messages test/broker-failover docs/retention"},
	{"grove", "feat/folder-sharing fix/upload-resume perf/thumbnail-cache test/trash-restore docs/storage-limits", "feat/file-versioning fix/signed-url-expiry test/multipart-upload docs/access-links"},
	{"harbor", "feat/image-signing fix/manifest-parser perf/layer-push test/registry-auth docs/mirror-setup", "feat/retention-rules fix/tag-deletion test/pull-through-cache docs/artifact-types"},
	{"iris", "feat/image-crop fix/exif-rotation perf/resize-worker test/avif-output docs/image-api", "feat/watermark-presets fix/alpha-channel test/color-profiles docs/transform-chain"},
	{"juniper", "feat/booking-calendar fix/timezone-slots test/overlapping-events chore/calendar-client docs/availability", "feat/recurring-bookings fix/cancelled-invites test/daylight-saving docs/calendar-sync"},
	{"kite", "feat/shipping-labels fix/tracking-webhook test/address-validation perf/rate-quotes docs/carrier-setup", "feat/return-labels fix/customs-values test/missing-postcode docs/shipment-events"},
	{"lumen", "feat/query-builder fix/date-buckets perf/dashboard-load test/csv-export docs/warehouse-setup", "feat/saved-reports fix/chart-legends test/empty-series docs/report-schedules"},
	{"maple", "feat/lesson-progress fix/quiz-scoring test/course-enrollment docs/content-authoring chore/video-player", "feat/certificates fix/resume-playback test/prerequisites docs/course-import"},
	{"nova", "feat/device-pairing fix/telemetry-gaps perf/sensor-batching test/firmware-update docs/device-protocol", "feat/offline-buffer fix/clock-drift test/reconnect docs/fleet-setup"},
	{"opal", "feat/product-variants fix/cart-quantity test/coupon-stacking perf/catalog-search docs/storefront-api", "feat/wishlist fix/stock-reservation test/guest-checkout docs/product-import"},
	{"prism", "feat/theme-editor fix/focus-ring test/contrast-ratios docs/component-examples chore/icon-set", "feat/density-controls fix/dialog-scroll test/keyboard-menu docs/design-tokens"},
	{"quartz", "feat/schedule-overrides fix/missed-ticks test/cron-timezones perf/job-dispatch docs/scheduler-config", "feat/run-history fix/lease-expiry test/concurrent-triggers docs/recovery"},
	{"relay", "feat/webhook-signatures fix/retry-jitter test/request-timeouts perf/connection-pool docs/event-payloads", "feat/delivery-replay fix/redirect-policy test/signature-rotation docs/rate-limits"},
	{"solstice", "feat/usage-metering fix/month-boundary test/plan-upgrades docs/subscription-api chore/pricing-import", "feat/trial-extension fix/proration test/cancel-renewal docs/billing-periods"},
	{"teams/platform-api", "feat/team-invitations fix/role-checks test/api-pagination perf/member-lookup docs/openapi-examples", "feat/audit-events fix/invite-expiry test/service-tokens docs/organization-roles"},
	{"teams/customer-dashboard", "feat/account-overview fix/mobile-navigation test/settings-form perf/activity-feed docs/dashboard-widgets", "feat/notification-preferences fix/avatar-upload test/empty-accounts docs/support-links"},
}

func fixtureUUID(key string) string {
	hash := sha256.Sum256([]byte("p-prototype/" + key))
	hash[6] = (hash[6] & 15) | 0x40
	hash[8] = (hash[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
}

func portfolioSessions() []session {
	var result []session
	for p, project := range portfolioProjects {
		for i, branch := range strings.Fields(project.sessions) {
			s := session{ID: fixtureUUID(project.name + "/" + branch), Project: project.name, Branch: branch,
				LastInteraction: 1788948000 - int64((p*5+i+1)*86400), Lifecycle: "stopped", Policy: "current", ProcessesKnown: true}
			active := (p == 0 && i < 4) || (p == 1 && i < 2) || (p == 2 && i < 2)
			if active {
				s.Lifecycle = "ready"
				s.LastInteraction = 1788948000 - int64((p*4+i)*420)
				task := strings.ReplaceAll(strings.SplitN(branch, "/", 2)[1], "-", " ")
				reason := "Implementing " + task
				signal := "running"
				if p == 0 && i == 0 {
					signal, reason = "attention", "Approve the device login flow before wiring the CLI"
				}
				if p == 1 && i == 0 {
					signal, reason = "attention", "Choose whether stale cache reads are acceptable during refresh"
				}
				s.Agents = []sessionProcess{{Name: "Codex", ID: fixtureUUID(s.ID + "/implementation"), Label: task,
					Description: "Work on " + task, State: "running", LastSignal: signal, LastReason: reason,
					Preview: []string{"You: Work on " + branch + " in " + project.name, "Codex: " + reason}}}
				// A focused regression session has two agents; documentation needs no service.
				if p == 0 && i == 3 {
					s.Agents = append(s.Agents, sessionProcess{Name: "Codex", ID: fixtureUUID(s.ID + "/review"), Label: "Expiry cases",
						Description: "Review token expiry edge cases", State: "running", LastSignal: "running", LastReason: "Checking expired device codes",
						Preview: []string{"You: Review token expiry edge cases.", "Codex: Checking expired device codes and slow polling."}})
				}
				if !strings.HasPrefix(branch, "docs/") {
					name, description, endpoint := "api.service", "Forge authentication API", ":3000"
					if p == 1 {
						name, description, endpoint = "worker.service", "Orbit queue worker", ""
					}
					if p == 2 {
						name, description, endpoint = "preview.service", "P session UI preview", ":8080"
					}
					s.Services = []sessionProcess{{Name: name, Description: description, State: "running", Endpoint: endpoint,
						Journal: []string{"10:00:00 systemd: Starting " + description, "10:00:01 " + name + ": Loaded project " + project.name,
							"10:00:02 " + name + ": Ready on branch " + branch}}}
				}
			}
			result = append(result, s)
		}
	}
	return result
}

func portfolioBranches() []retainedBranch {
	var result []retainedBranch
	for _, project := range portfolioProjects {
		branches := strings.Fields(project.branches)
		if project.name == "forge" {
			// Keep a longer branch picker in the main project to exercise paging.
			branches = append(branches, strings.Fields(`feat/token-introspection feat/session-revocation feat/organization-login
feat/recovery-codes feat/login-audit feat/scoped-api-keys feat/sso-discovery
fix/cookie-domain fix/device-code-expiry fix/polling-interval fix/refresh-token-rotation
fix/oauth-state-validation fix/callback-errors fix/clock-skew fix/redirect-allowlist
perf/token-lookup perf/key-cache test/passkey-fallback test/refresh-reuse
 test/login-rate-limit test/cross-tenant-access docs/device-setup docs/key-rotation
 docs/session-limits docs/sso-onboarding chore/oauth-client-upgrade refactor/token-store release/0.9`)...)
		}
		for _, branch := range branches {
			hash := sha256.Sum256([]byte(project.name + branch))
			result = append(result, retainedBranch{Project: project.name, Name: branch, Tip: fmt.Sprintf("%x", hash[:4])})
		}
	}
	return result
}
