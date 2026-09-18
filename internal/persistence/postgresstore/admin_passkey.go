package postgresstore

import "github.com/windcry1/ai-companion/internal/adminpasskey"

func (s *Store) AdminPasskeyStore() adminpasskey.Store { return adminpasskey.NewSQLStore(s.db, true) }
