-- The connection name approved in the browser, used when polling mints the
-- credential. Additive only: in-flight flows and legacy writers receive the
-- previous fixed label, and existing credentials are not relabelled.
ALTER TABLE login_flows_v2 ADD COLUMN credential_label TEXT NOT NULL DEFAULT 'Login Flow v2 connection';
