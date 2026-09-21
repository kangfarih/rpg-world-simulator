package quest

import (
	"database/sql"
	"log"
)

// ---------------------------------------------------------------------------
// Persistence (SQLite quests + achievements tables, m5 dirty-flush style).
// ---------------------------------------------------------------------------

// DB abstracts the quest/achievement tables. *sql.DB satisfies it; a nil DB
// disables persistence (dbConn == nil parity). The root adapter passes its
// dbConn through, converting a nil *sql.DB to a nil interface.
type DB interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

// EnsureTables creates the quest/achievement tables (M5 DDL order).
func EnsureTables(d Deps) {
	if d.DB == nil {
		return
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS quests(player TEXT, quest TEXT, stage INT, substage INT, PRIMARY KEY(player, quest))`,
		`CREATE TABLE IF NOT EXISTS achievements(player TEXT, ach TEXT, stage INT, PRIMARY KEY(player, ach))`,
	} {
		if _, err := d.DB.Exec(ddl); err != nil {
			log.Printf("m11: ddl: %v", err)
		}
	}
}

// PersistQuests writes the quest rows for one player (called from the
// disconnect/flush path).
func PersistQuests(d Deps, username string) {
	if d.DB == nil || username == "" {
		return
	}
	mu.Lock()
	st, found := states[username]
	mu.Unlock()
	if !found {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := d.DB.Exec(`DELETE FROM quests WHERE player=?`, username); err != nil {
		return
	}
	for key, q := range st.Quests {
		if _, err := d.DB.Exec(`INSERT INTO quests(player,quest,stage,substage) VALUES(?,?,?,?)`,
			username, key, q.Stage, q.SubStage); err != nil {
			log.Printf("m11: save quest %s: %v", key, err)
		}
	}
	if _, err := d.DB.Exec(`DELETE FROM achievements WHERE player=?`, username); err != nil {
		return
	}
	for key, stage := range st.Achs {
		if stage == 0 {
			continue
		}
		if _, err := d.DB.Exec(`INSERT INTO achievements(player,ach,stage) VALUES(?,?,?)`,
			username, key, stage); err != nil {
			log.Printf("m11: save achievement %s: %v", key, err)
		}
	}
}

// LoadQuests restores quest/achievement rows into the in-memory state
// (called before the login batches are built).
func LoadQuests(d Deps, username string) {
	Load()
	if !ok || d.DB == nil || username == "" {
		return
	}
	st := StateFor(username)
	rows, err := d.DB.Query(`SELECT quest,stage,substage FROM quests WHERE player=?`, username)
	if err == nil {
		for rows.Next() {
			var key string
			var stage, sub int
			if rows.Scan(&key, &stage, &sub) == nil {
				st.Quest(key).Stage = stage
				st.Quest(key).SubStage = sub
			}
		}
		rows.Close()
	}
	if rows, err := d.DB.Query(`SELECT ach,stage FROM achievements WHERE player=?`, username); err == nil {
		for rows.Next() {
			var key string
			var stage int
			if rows.Scan(&key, &stage) == nil {
				st.Achs[key] = stage
			}
		}
		rows.Close()
	}
}
