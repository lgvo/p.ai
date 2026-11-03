-- The schema of P's control state, created once on a fresh database.
--
-- P supports fresh installs only and never migrates state: edit this file
-- directly and raise schemaVersion in store.go with every change, so that
-- openStore refuses a database built from an earlier version.
--
-- Several triggers can guard one write; their order on a table decides which
-- refusal a write reports. Keep each table's triggers, and its indexes, in
-- the order they appear here.

-- Tables and their indexes

CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE projects (
 path TEXT PRIMARY KEY, policy_json TEXT NOT NULL, policy_sha256 TEXT NOT NULL,
 registry_state TEXT NOT NULL DEFAULT 'active' CHECK(registry_state IN ('active','deleting'))
);

CREATE TABLE sessions (
 uuid TEXT PRIMARY KEY, project_path TEXT NOT NULL REFERENCES projects(path),
 branch TEXT NOT NULL, registry_state TEXT NOT NULL CHECK(registry_state IN ('creating','established','removing')),
 policy_json TEXT NOT NULL, policy_sha256 TEXT NOT NULL,
 UNIQUE(project_path, branch)
);

CREATE TABLE operations (
 id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE,
 kind TEXT NOT NULL, project_path TEXT NOT NULL,
 session_uuid TEXT, request_json TEXT NOT NULL,
 request_sha256 TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('running','blocked','failed','unknown','superseded','completed')),
 phase TEXT NOT NULL, committed INTEGER NOT NULL DEFAULT 0 CHECK(committed IN (0,1)),
 evidence_json TEXT, diagnostic TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
-- One active operation per session, except that a narrowly proved blocked
-- creator shares its session with one assembled cleanup, which holds a
-- reversible guard on it until it commits.
CREATE UNIQUE INDEX active_session_operation ON operations(session_uuid)
 WHERE session_uuid IS NOT NULL AND status IN ('running','blocked','unknown')
 AND NOT (kind='session.create.cleanup' AND COALESCE(json_type(evidence_json,'$.review.runtime'),'')='object');
CREATE UNIQUE INDEX active_assembled_cleanup ON operations(session_uuid)
 WHERE status IN ('running','blocked','unknown') AND
 kind='session.create.cleanup' AND COALESCE(json_type(evidence_json,'$.review.runtime'),'')='object';
CREATE UNIQUE INDEX one_project_delete ON operations(project_path) WHERE kind='project.delete' AND status IN ('running','blocked','unknown');

CREATE TABLE git_principals (
 fingerprint TEXT PRIMARY KEY, role TEXT NOT NULL CHECK(role IN ('host','session')),
 project_path TEXT, session_uuid TEXT, active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
 CHECK((role='host' AND project_path IS NULL AND session_uuid IS NULL) OR
       (role='session' AND project_path IS NOT NULL AND session_uuid IS NOT NULL))
);
CREATE UNIQUE INDEX one_host_git_principal ON git_principals(role) WHERE role='host' AND active=1;
CREATE UNIQUE INDEX one_session_git_principal ON git_principals(session_uuid) WHERE role='session' AND active=1;

CREATE TABLE git_ref_guards (
 project_path TEXT NOT NULL, branch TEXT NOT NULL, operation_id TEXT NOT NULL REFERENCES operations(id),
 PRIMARY KEY(project_path,branch)
);

CREATE TABLE git_unborn_grants (
 session_uuid TEXT PRIMARY KEY REFERENCES sessions(uuid),
 state TEXT NOT NULL CHECK(state IN ('pending','consumed'))
);

CREATE TABLE session_unattended (
 session_uuid TEXT PRIMARY KEY REFERENCES sessions(uuid) ON DELETE CASCADE,
 condition TEXT NOT NULL CHECK(condition IN ('running','attention','idle','failed','unknown')),
 source TEXT NOT NULL, reason TEXT NOT NULL,
 adapter TEXT NOT NULL, adapter_version TEXT NOT NULL,
 received_at TEXT NOT NULL, receive_sequence INTEGER NOT NULL
);

CREATE TABLE status_sequence (id INTEGER PRIMARY KEY CHECK(id=1), next_value INTEGER NOT NULL);
INSERT INTO status_sequence(id,next_value) VALUES(1,1);

CREATE TABLE project_origins (
 project_path TEXT PRIMARY KEY REFERENCES projects(path) ON DELETE CASCADE,
 url TEXT NOT NULL, current_status TEXT NOT NULL CHECK(current_status IN ('fresh','unknown')),
 observed_at TEXT NOT NULL, refs_json TEXT NOT NULL, diagnostic TEXT NOT NULL DEFAULT ''
);

CREATE TABLE origin_requests (
 idempotency_key TEXT PRIMARY KEY, project_path TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('set','remove')),
 expected_url TEXT NOT NULL, proposed_url TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('prepared','completed')),
 created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', result_json TEXT NOT NULL DEFAULT ''
);

CREATE TABLE publication_requests (
 idempotency_key TEXT PRIMARY KEY, project_path TEXT NOT NULL,
 expected_origin_url TEXT NOT NULL,
 source_kind TEXT NOT NULL CHECK(source_kind IN ('session','retained')),
 source TEXT NOT NULL, source_oid TEXT NOT NULL, destination_ref TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('prepared','attempted','completed')),
 result_json TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE environment_images (
 project_path TEXT NOT NULL REFERENCES projects(path),
 environment_key TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 base_fingerprint TEXT NOT NULL,
 system TEXT NOT NULL,
 material_digest TEXT NOT NULL,
 capture_store_path TEXT NOT NULL,
 builder_request_uuid TEXT NOT NULL,
 properties_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 logical_size INTEGER NOT NULL DEFAULT 0 CHECK(logical_size >= 0),
 last_used_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(project_path, environment_key)
);
CREATE INDEX environment_images_fingerprint ON environment_images(fingerprint);

-- Public IPv4 slots are reserved with the session row, including a blank
-- project's bootstrap session. The four slots fit the confined Incus ceiling
-- and cannot be claimed twice by concurrent creation transactions.
CREATE TABLE session_public_addresses (
 session_uuid TEXT PRIMARY KEY REFERENCES sessions(uuid) ON DELETE CASCADE,
 slot INTEGER NOT NULL UNIQUE CHECK(slot BETWEEN 0 AND 3)
);

CREATE TABLE retired_lifecycle_requests (
 idempotency_key TEXT PRIMARY KEY, operation_id TEXT NOT NULL, kind TEXT NOT NULL,
 request_sha256 TEXT NOT NULL
);

-- Views

-- An assembled cleanup's guard holds only until its atomic DELETE
-- commitment, which alone retires the blocked creator and its authority.
CREATE VIEW assembled_cleanup_guards AS SELECT id,session_uuid FROM operations
 WHERE status IN ('running','blocked','unknown') AND
 kind='session.create.cleanup' AND committed=0 AND COALESCE(json_type(evidence_json,'$.review.runtime'),'')='object';

-- Triggers, by the table they guard

-- projects
CREATE TRIGGER deleting_project_policy BEFORE UPDATE ON projects
 WHEN OLD.registry_state='deleting'
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_registry_delete BEFORE DELETE ON projects
 WHEN OLD.registry_state='deleting' AND NOT EXISTS
 (SELECT 1 FROM operations WHERE project_path=OLD.path AND kind='project.delete' AND committed=1 AND phase='finalizing')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project resources incomplete'); END;

-- sessions
CREATE TRIGGER immutable_session_policy BEFORE UPDATE OF uuid,project_path,policy_json,policy_sha256 ON sessions
 BEGIN SELECT RAISE(ABORT, 'immutable session identity or policy'); END;
-- Rename reserves both names before any native effect. A concurrent create
-- cannot claim the new name while its guarded session row still names the old
-- branch.
CREATE TRIGGER session_insert_excludes_rename_reservation BEFORE INSERT ON sessions
 WHEN EXISTS(SELECT 1 FROM git_ref_guards g JOIN operations o ON o.id=g.operation_id
   WHERE g.project_path=NEW.project_path AND g.branch=NEW.branch
   AND o.kind IN ('session.rename','project.retained.rename','project.retained.delete')
   AND o.status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: branch reserved by lifecycle'); END;
CREATE TRIGGER assembled_cleanup_preserves_assignment BEFORE UPDATE OF registry_state,branch ON sessions
 WHEN EXISTS(SELECT 1 FROM assembled_cleanup_guards w WHERE w.session_uuid=OLD.uuid) OR
 EXISTS(SELECT 1 FROM operations w WHERE w.session_uuid=OLD.uuid AND w.kind='session.create.cleanup'
 AND w.status IN ('running','blocked','unknown') AND COALESCE(json_type(w.evidence_json,'$.review.runtime'),'')='object'
 AND NOT (w.committed=1 AND w.phase='delete-issued' AND OLD.branch=NEW.branch AND NEW.registry_state='removing'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: assembled cleanup active'); END;
CREATE TRIGGER assembled_cleanup_preserves_session BEFORE DELETE ON sessions
 WHEN EXISTS(SELECT 1 FROM assembled_cleanup_guards w WHERE w.session_uuid=OLD.uuid) OR
 EXISTS(SELECT 1 FROM operations w WHERE w.session_uuid=OLD.uuid AND w.kind='session.create.cleanup'
 AND w.status IN ('running','blocked','unknown') AND COALESCE(json_type(w.evidence_json,'$.review.runtime'),'')='object'
 AND NOT (w.committed=1 AND w.phase='local-complete' AND json_extract(w.evidence_json,'$.runtime_absent')=1
 AND json_extract(w.evidence_json,'$.local_complete')=1))
 BEGIN SELECT RAISE(ABORT,'constraint failed: assembled cleanup active'); END;
CREATE TRIGGER deleting_project_sessions_insert BEFORE INSERT ON sessions
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_sessions_update BEFORE UPDATE ON sessions
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=OLD.project_path AND registry_state='deleting') AND NOT
 (OLD.uuid=NEW.uuid AND OLD.project_path=NEW.project_path AND OLD.branch=NEW.branch AND NEW.registry_state='removing')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_sessions_delete BEFORE DELETE ON sessions
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=OLD.project_path AND registry_state='deleting') AND NOT EXISTS
 (SELECT 1 FROM operations WHERE project_path=OLD.project_path AND kind='project.delete' AND committed=1 AND phase='finalizing')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project resources incomplete'); END;

-- operations
CREATE TRIGGER lifecycle_request_key_conflict BEFORE INSERT ON operations
 WHEN EXISTS(SELECT 1 FROM origin_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER immutable_operation_request BEFORE UPDATE OF id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,created_at ON operations
 WHEN OLD.id != NEW.id OR OLD.idempotency_key != NEW.idempotency_key OR OLD.kind != NEW.kind OR OLD.project_path != NEW.project_path OR OLD.request_json != NEW.request_json OR OLD.request_sha256 != NEW.request_sha256 OR OLD.created_at != NEW.created_at
 OR (OLD.session_uuid IS NOT NEW.session_uuid AND NOT (OLD.kind='project.create' AND OLD.phase='repo-pending' AND OLD.session_uuid IS NULL AND NEW.session_uuid IS NOT NULL))
 BEGIN SELECT RAISE(ABORT, 'immutable operation request'); END;
CREATE TRIGGER operations_key_publication BEFORE INSERT ON operations
 WHEN EXISTS(SELECT 1 FROM publication_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER discard_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.discard' AND NOT EXISTS(
  SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
  AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: discard session unavailable'); END;
CREATE TRIGGER delete_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.delete' AND NOT EXISTS(
  SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
  AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: delete session unavailable'); END;
CREATE TRIGGER rename_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.rename' AND NOT EXISTS(
   SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
   AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: rename session unavailable'); END;
-- While an assembled cleanup guards a blocked creator, every other
-- active-operation exclusion stays in place, and the creator cannot advance,
-- even through a stale writer.
CREATE TRIGGER assembled_cleanup_excludes_other_insert BEFORE INSERT ON operations
 WHEN NEW.session_uuid IS NOT NULL AND NEW.status IN ('running','blocked','unknown') AND
 (EXISTS(SELECT 1 FROM assembled_cleanup_guards w WHERE w.session_uuid=NEW.session_uuid) OR
 EXISTS(SELECT 1 FROM operations w WHERE w.session_uuid=NEW.session_uuid AND w.kind='session.create.cleanup'
 AND w.status IN ('running','blocked','unknown') AND COALESCE(json_type(w.evidence_json,'$.review.runtime'),'')='object') OR
 (NEW.kind='session.create.cleanup' AND COALESCE(json_type(NEW.evidence_json,'$.review.runtime'),'')='object' AND
 EXISTS(SELECT 1 FROM operations o WHERE o.session_uuid=NEW.session_uuid AND o.status IN ('running','blocked','unknown')
 AND o.id!=json_extract(NEW.evidence_json,'$.review.old_operation_id'))))
 BEGIN SELECT RAISE(ABORT,'constraint failed: assembled cleanup active'); END;
CREATE TRIGGER assembled_cleanup_excludes_other_update BEFORE UPDATE ON operations
 WHEN EXISTS(SELECT 1 FROM assembled_cleanup_guards w WHERE w.id!=OLD.id AND w.session_uuid=OLD.session_uuid) OR
 EXISTS(SELECT 1 FROM operations w WHERE w.id!=OLD.id AND w.session_uuid=OLD.session_uuid AND w.kind='session.create.cleanup'
 AND w.status IN ('running','blocked','unknown') AND COALESCE(json_type(w.evidence_json,'$.review.runtime'),'')='object'
 AND NOT (w.committed=1 AND w.phase='delete-issued' AND OLD.kind='session.create'
 AND OLD.id=json_extract(w.evidence_json,'$.review.old_operation_id') AND NEW.status='superseded' AND NEW.phase='superseded'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: assembled cleanup active'); END;
CREATE TRIGGER assembled_cleanup_requires_creator BEFORE INSERT ON operations
 WHEN NEW.kind='session.create.cleanup' AND COALESCE(json_type(NEW.evidence_json,'$.review.runtime'),'')='object' AND NOT EXISTS(
 SELECT 1 FROM sessions s JOIN operations c ON c.id=json_extract(NEW.evidence_json,'$.review.old_operation_id')
 WHERE s.uuid=NEW.session_uuid AND s.project_path=NEW.project_path AND s.registry_state='creating'
 AND c.session_uuid=s.uuid AND c.project_path=s.project_path AND c.kind='session.create'
 AND c.status='blocked' AND c.phase='assembly-ready' AND c.committed=1
 AND c.request_sha256=json_extract(NEW.evidence_json,'$.review.old_request_sha256')
 AND NEW.committed=0 AND NEW.phase='validated')
 BEGIN SELECT RAISE(ABORT,'constraint failed: assembled cleanup source unavailable'); END;
CREATE TRIGGER deleting_project_operation_insert BEFORE INSERT ON operations
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting') AND
 NOT (NEW.kind='project.delete' AND NEW.committed=1 AND NEW.phase='ensure-absent')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_operation_update BEFORE UPDATE ON operations
 WHEN OLD.kind!='project.delete' AND EXISTS(SELECT 1 FROM projects WHERE path=OLD.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER operations_retired_key BEFORE INSERT ON operations
 WHEN EXISTS(SELECT 1 FROM retired_lifecycle_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: project-deleted request retired'); END;

-- git_principals
CREATE TRIGGER deleting_project_principal_insert BEFORE INSERT ON git_principals
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_principal_update BEFORE UPDATE ON git_principals
 WHEN NEW.active!=0 AND EXISTS(SELECT 1 FROM projects WHERE path=OLD.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;

-- git_ref_guards
CREATE TRIGGER deleting_project_git_ref_guards_insert BEFORE INSERT ON git_ref_guards
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_git_ref_guards_update BEFORE UPDATE ON git_ref_guards
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;

-- project_origins
CREATE TRIGGER deleting_project_project_origins_insert BEFORE INSERT ON project_origins
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_project_origins_update BEFORE UPDATE ON project_origins
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;

-- origin_requests
CREATE TRIGGER origin_request_key_conflict BEFORE INSERT ON origin_requests
 WHEN EXISTS(SELECT 1 FROM operations WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER origins_key_publication BEFORE INSERT ON origin_requests
 WHEN EXISTS(SELECT 1 FROM publication_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER deleting_project_origin_requests_insert BEFORE INSERT ON origin_requests
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_origin_requests_update BEFORE UPDATE ON origin_requests
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER origin_requests_retired_key BEFORE INSERT ON origin_requests
 WHEN EXISTS(SELECT 1 FROM retired_lifecycle_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: project-deleted request retired'); END;

-- publication_requests
CREATE TRIGGER publication_key_operations BEFORE INSERT ON publication_requests
 WHEN EXISTS(SELECT 1 FROM operations WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER publication_key_origins BEFORE INSERT ON publication_requests
 WHEN EXISTS(SELECT 1 FROM origin_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER deleting_project_publication_requests_insert BEFORE INSERT ON publication_requests
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_publication_requests_update BEFORE UPDATE ON publication_requests
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER publication_requests_retired_key BEFORE INSERT ON publication_requests
 WHEN EXISTS(SELECT 1 FROM retired_lifecycle_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: project-deleted request retired'); END;

-- environment_images
CREATE TRIGGER deleting_project_environment_images_insert BEFORE INSERT ON environment_images
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;
CREATE TRIGGER deleting_project_environment_images_update BEFORE UPDATE ON environment_images
 WHEN EXISTS(SELECT 1 FROM projects WHERE path=NEW.project_path AND registry_state='deleting')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project deletion authority closed'); END;

-- retired_lifecycle_requests
CREATE TRIGGER immutable_retired_request BEFORE UPDATE ON retired_lifecycle_requests
 BEGIN SELECT RAISE(ABORT,'immutable retired request'); END;
CREATE TRIGGER retain_retired_request BEFORE DELETE ON retired_lifecycle_requests
 BEGIN SELECT RAISE(ABORT,'retired request prevents replay'); END;
