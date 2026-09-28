package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY,
	state      TEXT    NOT NULL DEFAULT '',
	layers     TEXT    NOT NULL DEFAULT 'separate',
	format     TEXT    NOT NULL DEFAULT 'pdf',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS drafts (
	user_id    INTEGER PRIMARY KEY,
	title      TEXT    NOT NULL,
	skin       BLOB    NOT NULL,
	model      TEXT    NOT NULL,
	layers     TEXT    NOT NULL,
	format     TEXT    NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS crafts (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id         INTEGER NOT NULL,
	title           TEXT    NOT NULL,
	skin            BLOB    NOT NULL,
	model           TEXT    NOT NULL,
	layers          TEXT    NOT NULL,
	public          INTEGER NOT NULL DEFAULT 0,
	published_at    INTEGER NOT NULL DEFAULT 0,
	pdf_file_id     TEXT    NOT NULL DEFAULT '',
	png_file_ids    TEXT    NOT NULL DEFAULT '',
	preview_file_id TEXT    NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS crafts_user   ON crafts (user_id, id);
CREATE INDEX IF NOT EXISTS crafts_public ON crafts (public, published_at);
`

// Columns added after the first release; existing databases get them on start.
var migrations = []string{
	`ALTER TABLE drafts ADD COLUMN kind TEXT NOT NULL DEFAULT 'skin'`,
	`ALTER TABLE crafts ADD COLUMN kind TEXT NOT NULL DEFAULT 'skin'`,
}

// What a draft or craft was made from. Blockbench models are kept in the skin column.
const (
	kindSkin  = "skin"
	kindModel = "model"
)

type User struct {
	ID     int64
	State  string
	Layers string
	Format string
}

type Draft struct {
	UserID int64
	Kind   string
	Title  string
	Skin   []byte
	Model  string
	Layers string
	Format string
}

type Craft struct {
	ID            int64
	UserID        int64
	Kind          string
	Title         string
	Skin          []byte
	Model         string
	Layers        string
	Public        bool
	PDFFileID     string
	PNGFileIDs    string // comma-separated, one per page
	PreviewFileID string
	CreatedAt     time.Time
}

// Lists a card can be browsed from.
const (
	listMy      = "my"
	listGallery = "gal"
)

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) User(ctx context.Context, id int64) (*User, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, created_at) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, id, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	u := &User{ID: id}
	err = s.db.QueryRowContext(ctx, `SELECT state, layers, format FROM users WHERE id = ?`, id).
		Scan(&u.State, &u.Layers, &u.Format)
	return u, err
}

func (s *Store) SetState(ctx context.Context, id int64, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET state = ? WHERE id = ?`, state, id)
	return err
}

// SetPref remembers the last chosen layers/format so the next skin starts with them.
func (s *Store) SetPref(ctx context.Context, id int64, field, value string) error {
	if field != "layers" && field != "format" {
		return fmt.Errorf("unknown pref %q", field)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET `+field+` = ? WHERE id = ?`, value, id)
	return err
}

func (s *Store) SaveDraft(ctx context.Context, d *Draft) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drafts (user_id, kind, title, skin, model, layers, format, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET kind = excluded.kind, title = excluded.title, skin = excluded.skin,
			model = excluded.model, layers = excluded.layers, format = excluded.format, updated_at = excluded.updated_at`,
		d.UserID, kindOf(d.Kind), d.Title, d.Skin, d.Model, d.Layers, d.Format, time.Now().Unix())
	return err
}

// Draft returns the user's pending skin, or nil if there is none.
func (s *Store) Draft(ctx context.Context, userID int64) (*Draft, error) {
	d := &Draft{UserID: userID}
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, title, skin, model, layers, format FROM drafts WHERE user_id = ?`, userID).
		Scan(&d.Kind, &d.Title, &d.Skin, &d.Model, &d.Layers, &d.Format)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *Store) DeleteDraft(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM drafts WHERE user_id = ?`, userID)
	return err
}

func (s *Store) AddCraft(ctx context.Context, c *Craft) error {
	c.CreatedAt = time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO crafts (user_id, kind, title, skin, model, layers, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.UserID, kindOf(c.Kind), c.Title, c.Skin, c.Model, c.Layers, c.CreatedAt.Unix())
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

const craftColumns = `id, user_id, kind, title, skin, model, layers, public, pdf_file_id, png_file_ids, preview_file_id, created_at`

func scanCraft(row interface{ Scan(...any) error }) (*Craft, error) {
	c := &Craft{}
	var created int64
	err := row.Scan(&c.ID, &c.UserID, &c.Kind, &c.Title, &c.Skin, &c.Model, &c.Layers, &c.Public,
		&c.PDFFileID, &c.PNGFileIDs, &c.PreviewFileID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	c.CreatedAt = time.Unix(created, 0)
	return c, err
}

// Craft returns a craft by id, or nil if it does not exist.
func (s *Store) Craft(ctx context.Context, id int64) (*Craft, error) {
	return scanCraft(s.db.QueryRowContext(ctx, `SELECT `+craftColumns+` FROM crafts WHERE id = ?`, id))
}

// CraftAt returns the craft at position offset of a list (newest first) and the list length.
// The offset is clamped into range; the returned offset is the one actually used.
func (s *Store) CraftAt(ctx context.Context, list string, userID int64, offset int) (*Craft, int, int, error) {
	where, order, arg := `user_id = ?`, `id DESC`, any(userID)
	if list == listGallery {
		where, order, arg = `public = ?`, `published_at DESC, id DESC`, any(1)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crafts WHERE `+where, arg).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	if total == 0 {
		return nil, 0, 0, nil
	}
	offset = min(max(offset, 0), total-1)
	c, err := scanCraft(s.db.QueryRowContext(ctx,
		`SELECT `+craftColumns+` FROM crafts WHERE `+where+` ORDER BY `+order+` LIMIT 1 OFFSET ?`, arg, offset))
	return c, total, offset, err
}

func (s *Store) SetPublic(ctx context.Context, id int64, public bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE crafts SET public = ?, published_at = ? WHERE id = ?`,
		public, time.Now().UnixNano(), id)
	return err
}

func (s *Store) SetFileIDs(ctx context.Context, id int64, format, ids string) error {
	column := map[string]string{"pdf": "pdf_file_id", "png": "png_file_ids", "preview": "preview_file_id"}[format]
	if column == "" {
		return fmt.Errorf("unknown file kind %q", format)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE crafts SET `+column+` = ? WHERE id = ?`, ids, id)
	return err
}

func kindOf(kind string) string {
	if kind == "" {
		return kindSkin
	}
	return kind
}

func (s *Store) DeleteCraft(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM crafts WHERE id = ?`, id)
	return err
}
