package storage

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/internal/checker"
	_ "modernc.org/sqlite"
)

// Store 是 storage 包对外暴露的"存储句柄"。
// 内部持有一个 *sql.DB 连接池
type Store struct {
	db *sql.DB
}

// New 打开（或创建）数据库文件，创建表（如果不存在），返回 Store。
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// 建表。IF NOT EXISTS 让这个操作幂等——重复运行不会报错。
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS checks(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	target TEXT NOT NULL,
	status TEXT NOT NULL,
	latency_ms INTEGER,
	error TEXT,
	ts DATETIME DEFAULT CURRENT_TIMESTAMP
	)
	`)
	if err != nil {
		db.Close() // 建表失败要关连接
		return nil, err
	}

	// 加一个索引加速按 target 查询
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_checks_target ON checks(target)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS alerts(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_name TEXT NOT NULL,
	target TEXT NOT NULL,
	reason TEXT,
	fired_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alerts_target ON alerts(target)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	return &Store{db: db}, nil
}

// Save 保存单次检测结果。
func (s *Store) Save(r checker.Result) error {
	_, err := s.db.Exec(
		`INSERT INTO checks(target,status,latency_ms,error) VALUES (?,?,?,?)`,
		r.Target, r.Status, int64(r.Latency), r.Error,
	)
	return err
}

// GetRecent 返回最近 n 条记录，按时间倒序。
func (s *Store) GetRecent(n int) ([]checker.Result, error) {
	rows, err := s.db.Query(
		`SELECT target,status,latency_ms,error,ts FROM checks ORDER BY ts DESC LIMIT ?`,
		n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []checker.Result

	for rows.Next() {
		var r checker.Result
		var latencyNs int64
		if err := rows.Scan(&r.Target, &r.Status, &latencyNs, &r.Error, &r.Timestamp); err != nil {
			return nil, err
		}
		r.Latency = time.Duration(latencyNs)
		results = append(results, r)
	}
	// rows.Err() 检查迭代过程中是否出错（比如网络中断）
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetByTarget 返回某个 target 的历史记录。
func (s *Store) GetByTarget(target string, limit int) ([]checker.Result, error) {
	rows, err := s.db.Query(
		`SELECT target,status,latency_ms,error,ts FROM checks WHERE target=? ORDER BY ts DESC LIMIT ?`,
		target, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []checker.Result
	for rows.Next() {
		var r checker.Result
		var latencyNs int64
		if err := rows.Scan(&r.Target, &r.Status, &latencyNs, &r.Error, &r.Timestamp); err != nil {
			return nil, err
		}
		r.Latency = time.Duration(latencyNs)
		results = append(results, r)
	}
	// rows.Err() 检查迭代过程中是否出错（比如网络中断）
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// SaveBatch 批量保存结果，用事务保证原子性。
func (s *Store) SaveBatch(results []checker.Result) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Prepare 预编译一次 SQL，循环里重复使用
	stmt, err := tx.Prepare(
		`INSERT INTO checks (target,status,latency_ms,error) VALUES (?,?,?,?)`,
	)
	if err != nil {
		return err
	}

	defer stmt.Close()

	for _, r := range results {
		if _, err := stmt.Exec(r.Target, r.Status, int64(r.Latency), r.Error); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// GetLatestPerTarget 返回每个 target 的最新一条记录。
func (s *Store) GetLatestPerTarget() ([]checker.Result, error) {
	query := `
	SELECT c.target,c.status,c.latency_ms,c.error,c.ts
	FROM checks c
	INNER JOIN(
	SELECT target ,MAX(ts) AS max_ts
	FROM checks
	GROUP BY target
	) latest ON c.target=latest.target AND c.ts=latest.max_ts
	 ORDER BY c.target
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []checker.Result
	for rows.Next() {
		var r checker.Result
		var latencyNs int64
		if err := rows.Scan(&r.Target, &r.Status, &latencyNs, &r.Error, &r.Timestamp); err != nil {
			return nil, err
		}
		r.Latency = time.Duration(latencyNs)
		results = append(results, r)
	}
	return results, rows.Err()
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// 写告警
func (s *Store) SaveAlert(ev alert.Event) error {
	_, err := s.db.Exec(
		`INSERT INTO alerts (rule_name,target,reason,fired_at) VALUES (?,?,?,?)`,
		ev.RuleName, ev.Target, ev.Reason, ev.FireAt,
	)
	return err
}

// 读告警
func (s *Store) GetRecentAlerts(n int) ([]alert.Event, error) {
	rows, err := s.db.Query(
		`SELECT rule_name,target,reason,fired_at FROM alerts ORDER BY fired_at DESC LIMIT ?`, n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alert.Event
	for rows.Next() {
		var ev alert.Event
		if err := rows.Scan(&ev.RuleName, &ev.Target, &ev.Reason, &ev.FireAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
