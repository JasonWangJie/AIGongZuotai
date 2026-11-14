ALTER TABLE studio_users ADD COLUMN credential_version bigint NOT NULL DEFAULT 1 CHECK(credential_version>0);
ALTER TABLE studio_users ADD COLUMN email_verified_at timestamptz;
ALTER TABLE studio_sessions ADD COLUMN id text NOT NULL DEFAULT ('ses_'||replace(gen_random_uuid()::text,'-',''));
ALTER TABLE studio_sessions ADD CONSTRAINT studio_session_public_id UNIQUE(id);
ALTER TABLE studio_sessions ADD COLUMN credential_version bigint NOT NULL DEFAULT 1 CHECK(credential_version>0);
CREATE INDEX studio_sessions_user_created ON studio_sessions(user_id,created_at DESC,id DESC);
CREATE TABLE studio_account_tokens (
 token_hash bytea PRIMARY KEY CHECK(octet_length(token_hash)=32),
 user_id text NOT NULL REFERENCES studio_users(id),
 purpose text NOT NULL CHECK(purpose IN ('verify_email','reset_password')),
 credential_version bigint NOT NULL CHECK(credential_version>0),
 expires_at timestamptz NOT NULL,
 used_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX studio_account_tokens_user ON studio_account_tokens(user_id,purpose,created_at DESC);
CREATE TABLE studio_security_events (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES studio_users(id),
 action text NOT NULL CHECK(action IN ('account.registered','account.login','account.logout','email.verification_requested','email.verified','password.reset_requested','password.reset','password.changed','session.revoked','sessions.revoked_all')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX studio_security_events_page ON studio_security_events(user_id,created_at DESC,id DESC);
CREATE TABLE studio_mail_outbox (
 id text PRIMARY KEY,
 purpose text NOT NULL CHECK(purpose IN ('verify_email','reset_password')),
 key_id text NOT NULL,
 ciphertext bytea NOT NULL,
 expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','RETRY','SENT','FAILED','SUBMIT_UNKNOWN','EXPIRED')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX studio_mail_outbox_pending ON studio_mail_outbox(next_attempt_at,id) WHERE state IN ('PENDING','RETRY');
CREATE INDEX studio_mail_outbox_active ON studio_mail_outbox(expires_at,id) WHERE state IN ('PENDING','SENDING','RETRY','SUBMIT_UNKNOWN');
CREATE INDEX studio_mail_outbox_cleanup ON studio_mail_outbox(updated_at,id) WHERE state IN ('SENT','FAILED','EXPIRED');
