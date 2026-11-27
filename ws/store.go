package main

import (
	"context"
	"database/sql"
	"errors"

	_ "github.com/go-sql-driver/mysql"
)

type Store struct {
	db *sql.DB
}

func NewStore(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) UserID(ctx context.Context, token string) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id FROM sessions s JOIN users u ON s.user_id = u.id
		 WHERE s.token = ? AND u.is_active = 1 LIMIT 1`, token).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE token = ? AND is_active = 1 LIMIT 1`, token).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errUnauthorized
	}
	return id, err
}

var errUnauthorized = errors.New("登录已失效")

func (s *Store) PmMaxID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM social_pm_messages`).Scan(&id)
	return id, err
}

func (s *Store) GroupMaxID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM social_messages`).Scan(&id)
	return id, err
}

func (s *Store) PmAfter(ctx context.Context, after int64, limit int) ([]PmMsg, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.conv_id, m.from_user, m.to_user,
		       COALESCE(m.content,''), COALESCE(m.image,''), COALESCE(m.image_w,0), COALESCE(m.image_h,0),
		       COALESCE(m.video,''), COALESCE(m.video_w,0), COALESCE(m.video_h,0),
		       COALESCE(m.video_duration,0), COALESCE(m.video_size,0),
		       COALESCE(m.msg_type,''), COALESCE(m.created_at,''),
		       COALESCE(u.nickname,''), COALESCE(u.username,'')
		FROM social_pm_messages m JOIN users u ON u.id = m.from_user
		WHERE m.id > ? AND m.is_recalled = 0
		ORDER BY m.id ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PmMsg
	for rows.Next() {
		var m PmMsg
		if err := rows.Scan(&m.ID, &m.ConvID, &m.FromUser, &m.ToUser,
			&m.Content, &m.Image, &m.ImageW, &m.ImageH,
			&m.Video, &m.VideoW, &m.VideoH, &m.VideoDuration, &m.VideoSize,
			&m.MsgType, &m.CreatedAt, &m.Nickname, &m.Username); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GroupAfter(ctx context.Context, after int64, limit int) ([]GroupMsg, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.group_id, COALESCE(g.name,''), m.user_id,
		       COALESCE(m.content,''), COALESCE(m.image,''), COALESCE(m.image_w,0), COALESCE(m.image_h,0),
		       COALESCE(m.video,''), COALESCE(m.video_w,0), COALESCE(m.video_h,0),
		       COALESCE(m.video_duration,0), COALESCE(m.video_size,0),
		       COALESCE(m.at_users,''), COALESCE(m.msg_type,''), COALESCE(m.created_at,''),
		       COALESCE(u.nickname,''), COALESCE(u.username,'')
		FROM social_messages m
		JOIN users u ON u.id = m.user_id
		LEFT JOIN social_groups g ON g.id = m.group_id
		WHERE m.id > ? AND m.is_recalled = 0
		ORDER BY m.id ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupMsg
	for rows.Next() {
		var m GroupMsg
		var atUsers string
		if err := rows.Scan(&m.ID, &m.GroupID, &m.GroupName, &m.UserID,
			&m.Content, &m.Image, &m.ImageW, &m.ImageH,
			&m.Video, &m.VideoW, &m.VideoH, &m.VideoDuration, &m.VideoSize,
			&atUsers, &m.MsgType, &m.CreatedAt, &m.Nickname, &m.Username); err != nil {
			return nil, err
		}
		m.AtUsers = atUsers
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GroupMembers(ctx context.Context, groupID int) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM social_group_members WHERE group_id = ?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var uid int
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out[uid] = true
	}
	return out, rows.Err()
}

func (s *Store) PmPeer(ctx context.Context, convID, me int) (int, error) {
	var a, b int
	err := s.db.QueryRowContext(ctx, `SELECT user_a, user_b FROM social_pms WHERE id = ?`, convID).Scan(&a, &b)
	if err != nil {
		return 0, err
	}
	if a == me {
		return b, nil
	}
	if b == me {
		return a, nil
	}
	return 0, errors.New("会话不存在")
}

func (s *Store) IsMuted(ctx context.Context, userID, groupID int) bool {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM social_mutes WHERE user_id = ? AND group_id = ? LIMIT 1`,
		userID, groupID).Scan(&one)
	return err == nil
}

func (s *Store) SetPmRead(ctx context.Context, userID, convID int, lastID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_pm_reads SET last_read_id = GREATEST(COALESCE(last_read_id,0), ?), updated_at = NOW()
		 WHERE user_id = ? AND conv_id = ?`, lastID, userID, convID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO social_pm_reads (user_id, conv_id, last_read_id, updated_at)
		 VALUES (?, ?, ?, NOW())`, userID, convID, lastID)
	return err
}

func (s *Store) SetGroupRead(ctx context.Context, userID, groupID int, lastID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE social_reads SET last_read_id = GREATEST(COALESCE(last_read_id,0), ?), updated_at = NOW()
		 WHERE user_id = ? AND group_id = ?`, lastID, userID, groupID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO social_reads (user_id, group_id, last_read_id, updated_at)
		 VALUES (?, ?, ?, NOW())`, userID, groupID, lastID)
	return err
}

type ClientMsg struct {
	MsgID     int64
	ConvID    int
	GroupID   int
	CreatedAt string
}

func (s *Store) LookupClientMsg(ctx context.Context, userID int, clientMsgID string) (*ClientMsg, error) {
	var m ClientMsg
	err := s.db.QueryRowContext(ctx,
		`SELECT msg_id, conv_id, group_id, DATE_FORMAT(created_at,'%Y-%m-%d %H:%i:%s')
		 FROM ws_client_msgs WHERE user_id = ? AND client_msg_id = ? LIMIT 1`,
		userID, clientMsgID).Scan(&m.MsgID, &m.ConvID, &m.GroupID, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) SaveClientMsg(ctx context.Context, userID int, clientMsgID string, m ClientMsg) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ws_client_msgs (user_id, client_msg_id, msg_id, conv_id, group_id)
		 VALUES (?, ?, ?, ?, ?)`,
		userID, clientMsgID, m.MsgID, m.ConvID, m.GroupID)
	return err
}
