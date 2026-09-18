// admin-invite provisions the first administrator or recovers a lost passkey
// using operator-controlled database access. No public bootstrap secret exists.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/adminpasskey"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/persistence"
)

func main() {
	id := flag.String("id", "", "administrator account ID")
	name := flag.String("name", "", "display name (required for new accounts)")
	role := flag.String("role", "admin", "admin, support or viewer (new accounts only)")
	reason := flag.String("reason", "", "required audit reason")
	recoverAccount := flag.Bool("recover", false, "revoke all existing passkeys, tokens and sessions before issuing a recovery invitation")
	flag.Parse()
	if strings.TrimSpace(*id) == "" || len(*id) > 128 || strings.TrimSpace(*reason) == "" {
		log.Fatal("--id and --reason are required; id must be at most 128 characters")
	}
	driver := os.Getenv("DATABASE_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := persistence.Open(ctx, driver, os.Getenv("MYSQL_DSN"), os.Getenv("POSTGRES_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	origin := os.Getenv("WEB_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	passkeys, err := adminpasskey.New(store.AdminPasskeyStore(), store, origin)
	if err != nil {
		log.Fatal(err)
	}
	accounts := opsauth.NewService(store)
	a, err := accounts.Get(ctx, *id)
	if errors.Is(err, opsauth.ErrNotFound) {
		created, createErr := accounts.Create(ctx, opsauth.CreateInput{ID: *id, DisplayName: *name, Role: *role, Actor: "admin-invite-cli", Reason: *reason})
		if createErr != nil {
			log.Fatal(createErr)
		}
		a = created.Account
	} else if err != nil {
		log.Fatal(err)
	}
	if a.Status != "active" {
		log.Fatal("account is disabled; enable it through an authorized administrator first")
	}
	if passkeys.HasCredential(ctx, a.ID) && !*recoverAccount {
		log.Fatal("account already has a passkey; add a backup in /admin, or explicitly use --recover")
	}
	if *recoverAccount {
		recovery, ok := passkeys.Store.(adminpasskey.RecoveryStore)
		if !ok {
			log.Fatal("atomic recovery requires a persistent database")
		}
		if err = recovery.RecoverAccount(ctx, a.ID, *reason); err != nil {
			log.Fatal(err)
		}
	} else {
		// Reissuing an invitation invalidates earlier incomplete enrollment attempts.
		if _, err = accounts.ResetToken(ctx, a.ID, "admin-invite-cli", *reason); err != nil {
			log.Fatal(err)
		}
		for _, kind := range []string{"invite", "register"} {
			if err = passkeys.Store.DeleteOwner(ctx, kind, a.ID); err != nil {
				log.Fatal(err)
			}
		}
	}
	token, err := passkeys.IssueInvite(ctx, a.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("一次性绑定链接（30 分钟有效，请安全传递）：\n%s/admin#invite=%s\n", passkeys.Origin, token)
}
