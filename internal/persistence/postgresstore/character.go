package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
)

var _ character.Store = (*Store)(nil)

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
	rows, err := s.db.QueryContext(ctx, characterSelect+` WHERE user_id=$1 AND status='active' ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]character.Character, 0)
	for rows.Next() {
		item, scanErr := scanCharacter(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Get(ctx context.Context, userID, characterID string) (character.Character, error) {
	item, err := scanCharacter(s.db.QueryRowContext(ctx, characterSelect+` WHERE id=$1 AND user_id=$2 AND status='active'`, characterID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = character.ErrNotFound
	}
	return item, err
}

func (s *Store) Update(ctx context.Context, item character.Character, persona character.PersonaVersion) error {
	hobbies, err := json.Marshal(item.Hobbies)
	if err != nil {
		return err
	}
	boundaries, err := json.Marshal(item.Boundaries)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE app.characters SET
			module_key=$1,name=$2,avatar_url=$3,relationship_label=$4,
			personality=$5,speech_style=$6,hobbies=$7,boundaries=$8,
			initiative=$9,reply_length=$10,sticker_style=$11,raw_prompt=$12,
			persona_version=$13,updated_at=$14
		WHERE id=$15 AND user_id=$16 AND status='active'`,
		item.Module, item.Name, nullString(item.AvatarURL), item.Relationship,
		item.Personality, item.SpeechStyle, hobbies, boundaries, item.Initiative,
		item.ReplyLength, item.StickerStyle, item.RawPrompt, item.PersonaVersion,
		item.UpdatedAt, item.ID, item.UserID,
	)
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
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.characters
		SET status='deleted',updated_at=$1
		WHERE id=$2 AND user_id=$3 AND status='active'`,
		now, characterID, userID,
	)
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.character_id::text,v.version,v.compiler_version,v.compiled_persona,v.created_at
		FROM app.character_persona_versions v
		JOIN app.characters c ON c.id=v.character_id
		WHERE v.character_id=$1 AND c.user_id=$2 AND c.status='active'
		ORDER BY v.version`,
		characterID, userID,
	)
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

const characterSelect = `
	SELECT
		id::text,user_id::text,module_key,name,COALESCE(avatar_url,''),
		relationship_label,personality,speech_style,hobbies,boundaries,
		initiative,reply_length,sticker_style,raw_prompt,persona_version,
		status,created_at,updated_at
	FROM app.characters`

func insertCharacter(ctx context.Context, tx *sql.Tx, item character.Character) error {
	hobbies, err := json.Marshal(item.Hobbies)
	if err != nil {
		return err
	}
	boundaries, err := json.Marshal(item.Boundaries)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.characters (
			id,user_id,module_key,name,avatar_url,relationship_label,personality,
			speech_style,hobbies,boundaries,initiative,reply_length,sticker_style,
			raw_prompt,persona_version,status,created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
		)`,
		item.ID, item.UserID, item.Module, item.Name, nullString(item.AvatarURL),
		item.Relationship, item.Personality, item.SpeechStyle, hobbies, boundaries,
		item.Initiative, item.ReplyLength, item.StickerStyle, item.RawPrompt,
		item.PersonaVersion, item.Status, item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func insertPersona(ctx context.Context, tx *sql.Tx, item character.PersonaVersion) error {
	compiled, err := json.Marshal(item.Compiled)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.character_persona_versions (
			character_id,version,compiler_version,compiled_persona,created_at
		) VALUES ($1,$2,$3,$4,$5)`,
		item.CharacterID, item.Version, item.CompilerVersion, compiled, item.CreatedAt,
	)
	return err
}

func scanCharacter(row rowScanner) (character.Character, error) {
	var item character.Character
	var hobbies, boundaries []byte
	err := row.Scan(
		&item.ID, &item.UserID, &item.Module, &item.Name, &item.AvatarURL,
		&item.Relationship, &item.Personality, &item.SpeechStyle, &hobbies,
		&boundaries, &item.Initiative, &item.ReplyLength, &item.StickerStyle,
		&item.RawPrompt, &item.PersonaVersion, &item.Status, &item.CreatedAt,
		&item.UpdatedAt,
	)
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
