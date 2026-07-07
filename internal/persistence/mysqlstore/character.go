package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
)

func (s *Store) Create(ctx context.Context, item character.Character, persona character.PersonaVersion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertCharacter(ctx, tx, item); err != nil {
		return err
	}
	if err = insertPersona(ctx, tx, persona); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) List(ctx context.Context, userID string) ([]character.Character, error) {
	rows, err := s.db.QueryContext(ctx, characterSelect+` WHERE user_id=UUID_TO_BIN(?) AND status='active' ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]character.Character, 0)
	for rows.Next() {
		item, err := scanCharacter(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Get(ctx context.Context, userID, characterID string) (character.Character, error) {
	item, err := scanCharacter(s.db.QueryRowContext(ctx, characterSelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, characterID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = character.ErrNotFound
	}
	return item, err
}

func (s *Store) Update(ctx context.Context, item character.Character, persona character.PersonaVersion) error {
	hobbies, _ := json.Marshal(item.Hobbies)
	boundaries, _ := json.Marshal(item.Boundaries)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE characters SET name=?,avatar_url=?,relationship_label=?,personality=?,speech_style=?,hobbies=?,boundaries=?,initiative=?,reply_length=?,sticker_style=?,raw_prompt=?,persona_version=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, item.Name, nullString(item.AvatarURL), item.Relationship, item.Personality, item.SpeechStyle, hobbies, boundaries, item.Initiative, item.ReplyLength, item.StickerStyle, item.RawPrompt, item.PersonaVersion, item.UpdatedAt, item.ID, item.UserID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return character.ErrNotFound
	}
	if err = insertPersona(ctx, tx, persona); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Delete(ctx context.Context, userID, characterID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE characters SET status='deleted',updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, now, characterID, userID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return character.ErrNotFound
	}
	return nil
}

func (s *Store) ListPersonaVersions(ctx context.Context, userID, characterID string) ([]character.PersonaVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT BIN_TO_UUID(v.character_id),v.version,v.compiler_version,v.compiled_persona,v.created_at FROM character_persona_versions v JOIN characters c ON c.id=v.character_id WHERE v.character_id=UUID_TO_BIN(?) AND c.user_id=UUID_TO_BIN(?) AND c.status='active' ORDER BY v.version`, characterID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]character.PersonaVersion, 0)
	for rows.Next() {
		var item character.PersonaVersion
		var compiled []byte
		if err := rows.Scan(&item.CharacterID, &item.Version, &item.CompilerVersion, &compiled, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(compiled, &item.Compiled); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil, character.ErrNotFound
	}
	return result, rows.Err()
}

const characterSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),name,COALESCE(avatar_url,''),relationship_label,personality,speech_style,hobbies,boundaries,initiative,reply_length,sticker_style,raw_prompt,persona_version,status,created_at,updated_at FROM characters`

func insertCharacter(ctx context.Context, tx *sql.Tx, item character.Character) error {
	hobbies, _ := json.Marshal(item.Hobbies)
	boundaries, _ := json.Marshal(item.Boundaries)
	_, err := tx.ExecContext(ctx, `INSERT INTO characters (id,user_id,name,avatar_url,relationship_label,personality,speech_style,hobbies,boundaries,initiative,reply_length,sticker_style,raw_prompt,persona_version,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.UserID, item.Name, nullString(item.AvatarURL), item.Relationship, item.Personality, item.SpeechStyle, hobbies, boundaries, item.Initiative, item.ReplyLength, item.StickerStyle, item.RawPrompt, item.PersonaVersion, item.Status, item.CreatedAt, item.UpdatedAt)
	return err
}
func insertPersona(ctx context.Context, tx *sql.Tx, item character.PersonaVersion) error {
	compiled, err := json.Marshal(item.Compiled)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO character_persona_versions (character_id,version,compiler_version,compiled_persona,created_at) VALUES (UUID_TO_BIN(?),?,?,?,?)`, item.CharacterID, item.Version, item.CompilerVersion, compiled, item.CreatedAt)
	return err
}
func scanCharacter(row rowScanner) (character.Character, error) {
	var item character.Character
	var hobbies, boundaries []byte
	err := row.Scan(&item.ID, &item.UserID, &item.Name, &item.AvatarURL, &item.Relationship, &item.Personality, &item.SpeechStyle, &hobbies, &boundaries, &item.Initiative, &item.ReplyLength, &item.StickerStyle, &item.RawPrompt, &item.PersonaVersion, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return item, err
	}
	if err = json.Unmarshal(hobbies, &item.Hobbies); err != nil {
		return item, err
	}
	if err = json.Unmarshal(boundaries, &item.Boundaries); err != nil {
		return item, err
	}
	return item, nil
}
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
