package store

// migrations are applied in order, each once, with foreign keys switched off
// for the duration (see migrate). NEVER edit one that has been released: add
// a new one. A database is migrated on every start, so an edited migration
// would never run on a database that already has it — and migration 1 is in
// every central.db the 1.0 lab left behind.
var migrations = []string{
	// 1 — version 1.0 (DESIGN.md §8), exactly as it was released.
	`CREATE TABLE instances (
		id                TEXT PRIMARY KEY,
		label             TEXT NOT NULL DEFAULT '',
		token_hash        TEXT NOT NULL UNIQUE,
		created_at        TEXT NOT NULL,
		last_report_at    TEXT,
		agent_version     TEXT NOT NULL DEFAULT '',
		juiceshop_version TEXT NOT NULL DEFAULT '',
		reachable         INTEGER NOT NULL DEFAULT 0,
		restore_failed    INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE participants (
		id              INTEGER PRIMARY KEY,
		instance_id     TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		registration_id TEXT NOT NULL,
		name            TEXT NOT NULL,
		registered_at   TEXT NOT NULL,
		ended_at        TEXT,
		hidden          INTEGER NOT NULL DEFAULT 0,
		UNIQUE (instance_id, registration_id)
	);
	CREATE TABLE challenges (
		key        TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		category   TEXT NOT NULL,
		difficulty INTEGER NOT NULL,
		disabled   INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE solves (
		participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
		challenge_key  TEXT NOT NULL,
		solved_at      TEXT NOT NULL,
		cheat          REAL,
		PRIMARY KEY (participant_id, challenge_key)
	);
	CREATE INDEX solves_by_time ON solves (solved_at);
	CREATE TABLE commands (
		id           TEXT PRIMARY KEY,
		instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		type         TEXT NOT NULL,
		created_at   TEXT NOT NULL,
		delivered_at TEXT,
		confirmed_at TEXT
	);
	CREATE TABLE scoring (
		difficulty INTEGER PRIMARY KEY,
		points     INTEGER NOT NULL
	);
	INSERT INTO scoring (difficulty, points) VALUES (1,100),(2,250),(3,450),(4,700),(5,1000),(6,1350);`,

	// 2 — version 1.1: games, a token per participant, commands per
	// participant (VERSION-1-1.md §5.1, §8).
	//
	// Every 1.0 participant moves into ONE closed game "1.0", and ended_at
	// is NOT carried over: in the lab, 1.0's rule "a new registration ends
	// the previous participant of the instance" ended every student but the
	// last (VERSION-1-1.md §1.1), so ended_at says nothing about the
	// student. Clearing it puts the whole lab back on the "1.0" board. Ids
	// are kept, so the solves keep pointing at their participant.
	//
	// The tables are rebuilt (SQLite's "create new, copy, drop, rename"),
	// which is why migrate() runs with foreign keys off: with them on,
	// DROP TABLE participants would cascade into solves.
	`CREATE TABLE games (
		id             INTEGER PRIMARY KEY,
		name           TEXT NOT NULL,
		slug           TEXT NOT NULL UNIQUE,
		code_hash      TEXT UNIQUE,
		state          TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'open', 'running', 'closed')),
		starts_at      TEXT,
		ends_at        TEXT,
		closed_at      TEXT,
		board_hidden   INTEGER NOT NULL DEFAULT 0,
		board_initials INTEGER NOT NULL DEFAULT 0,
		created_at     TEXT NOT NULL
	);
	CREATE TABLE game_scoring (
		game_id    INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		difficulty INTEGER NOT NULL,
		points     INTEGER NOT NULL,
		PRIMARY KEY (game_id, difficulty)
	);
	INSERT INTO games (name, slug, state, created_at, closed_at)
		SELECT '1.0', '1-0', 'closed', MIN(registered_at),
		       COALESCE((SELECT MAX(solved_at) FROM solves), MAX(registered_at))
		FROM participants HAVING COUNT(*) > 0;
	INSERT INTO game_scoring (game_id, difficulty, points)
		SELECT g.id, s.difficulty, s.points FROM games g, scoring s WHERE g.slug = '1-0';

	CREATE TABLE participants_v2 (
		id                   INTEGER PRIMARY KEY,
		game_id              INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		name                 TEXT NOT NULL,
		token_hash           TEXT UNIQUE,
		node_uid             TEXT NOT NULL DEFAULT '',
		joined_at            TEXT NOT NULL,
		node_joined_at       TEXT,
		left_at              TEXT,
		left_reason          TEXT NOT NULL DEFAULT '',
		hidden               INTEGER NOT NULL DEFAULT 0,
		last_report_at       TEXT,
		last_report_event_at TEXT,
		node_version         TEXT NOT NULL DEFAULT '',
		juiceshop_version    TEXT NOT NULL DEFAULT '',
		reachable            INTEGER NOT NULL DEFAULT 0,
		restore_failed       INTEGER NOT NULL DEFAULT 0,
		boot                 TEXT NOT NULL DEFAULT '',
		legacy_instance      TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO participants_v2 (id, game_id, name, joined_at, hidden, last_report_at,
			node_version, juiceshop_version, reachable, restore_failed, legacy_instance)
		SELECT p.id, (SELECT id FROM games WHERE slug = '1-0'), p.name, p.registered_at, p.hidden,
		       i.last_report_at, COALESCE(i.agent_version, ''), COALESCE(i.juiceshop_version, ''),
		       COALESCE(i.reachable, 0), COALESCE(i.restore_failed, 0), p.instance_id
		FROM participants p LEFT JOIN instances i ON i.id = p.instance_id;

	CREATE TABLE solves_v2 (
		participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
		challenge_key  TEXT NOT NULL,
		solved_at      TEXT NOT NULL,
		received_at    TEXT NOT NULL,
		cheat          REAL,
		counted        INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (participant_id, challenge_key)
	);
	INSERT INTO solves_v2 (participant_id, challenge_key, solved_at, received_at, cheat, counted)
		SELECT participant_id, challenge_key, solved_at, solved_at, cheat, 1 FROM solves;

	DROP TABLE solves;
	DROP TABLE commands;
	DROP TABLE participants;
	DROP TABLE instances;
	DROP TABLE scoring;
	ALTER TABLE participants_v2 RENAME TO participants;
	ALTER TABLE solves_v2 RENAME TO solves;
	CREATE INDEX participants_by_game ON participants (game_id);
	CREATE INDEX solves_by_received ON solves (received_at);

	CREATE TABLE commands (
		id             TEXT PRIMARY KEY,
		token_hash     TEXT NOT NULL,
		participant_id INTEGER,
		game_id        INTEGER,
		type           TEXT NOT NULL,
		created_at     TEXT NOT NULL,
		delivered_at   TEXT,
		confirmed_at   TEXT
	);
	CREATE INDEX commands_by_token ON commands (token_hash);

	CREATE TABLE participant_boots (
		participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
		boot           TEXT NOT NULL,
		first_seen     TEXT NOT NULL,
		last_seen      TEXT NOT NULL,
		PRIMARY KEY (participant_id, boot)
	);`,

	// 3 — the event feed, settings and flags (VERSION-1-1.md §5.4, §5.5).
	// events has no foreign keys on purpose: what happened in a game stays in
	// the feed after the game is deleted, until the retention removes it.
	`CREATE TABLE events (
		id             INTEGER PRIMARY KEY,
		at             TEXT NOT NULL,
		kind           TEXT NOT NULL,
		game_id        INTEGER,
		participant_id INTEGER,
		address        TEXT NOT NULL DEFAULT '',
		text           TEXT NOT NULL
	);
	CREATE INDEX events_by_time ON events (at);
	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	INSERT INTO settings (key, value) VALUES
		('event_retention_days', '30'), ('title', 'Juice Shop CTF'), ('timezone', 'Europe/Amsterdam');
	CREATE TABLE flags (
		participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
		kind           TEXT NOT NULL,
		detail         TEXT NOT NULL DEFAULT '',
		at             TEXT NOT NULL,
		PRIMARY KEY (participant_id, kind, detail)
	);`,

	// 4 — admin accounts (users.go). Until 1.1 /admin/ had no login and nginx
	// was the only lock; now the application has its own users, made on the
	// setup page while there are none and on the users page after that.
	//
	// username is unique WITHOUT regard to case, so "Maurice" and "maurice"
	// cannot be two accounts that look the same in the event feed. A session
	// is stored by the SHA-256 of its cookie, like a participant token: a
	// copy of this database logs nobody in. Deleting a user deletes their
	// sessions (the cascade), which is what makes "delete" log them out.
	//
	// events.actor is who did an admin action, so that with several
	// operators the feed says WHICH one cleared a game.
	`CREATE TABLE users (
		id                  INTEGER PRIMARY KEY,
		username            TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash       TEXT NOT NULL,
		created_at          TEXT NOT NULL,
		created_by          TEXT NOT NULL DEFAULT '',
		password_changed_at TEXT NOT NULL,
		last_login_at       TEXT
	);
	CREATE TABLE sessions (
		token_hash   TEXT PRIMARY KEY,
		user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at   TEXT NOT NULL,
		last_seen_at TEXT NOT NULL,
		expires_at   TEXT NOT NULL,
		remember     INTEGER NOT NULL DEFAULT 0,
		address      TEXT NOT NULL DEFAULT '',
		user_agent   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX sessions_by_user ON sessions (user_id);
	ALTER TABLE events ADD COLUMN actor TEXT NOT NULL DEFAULT '';`,
}
