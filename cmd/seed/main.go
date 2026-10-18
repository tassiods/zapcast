// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

// Command seed populates store/zapcast.db with fake contacts and groups for
// UI/UX stress testing. It writes through the ContactStore (the same path
// the HTTP handlers use) so the seeded shape mirrors production exactly —
// no schema drift, no surprises when the tests later read the rows back.
//
// Usage:
//
//	go run ./cmd/seed -contacts=1000 -groups=100
//	go run ./cmd/seed -reset    # wipe existing data first
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"

	"zapcast/internal/server"
)

// Names drawn from common Brazilian first/last names to make the seeded
// contacts look realistic in the UI. No real people are referenced.
var firstNames = []string{
	"João", "Maria", "Pedro", "Ana", "Carlos", "Júlia", "Lucas", "Mariana",
	"Rafael", "Beatriz", "Gabriel", "Camila", "Bruno", "Fernanda", "Diego",
	"Patrícia", "Thiago", "Larissa", "Ricardo", "Amanda", "Felipe", "Juliana",
	"Rodrigo", "Vanessa", "Gustavo", "Letícia", "André", "Carolina", "Daniel",
	"Isabela", "Eduardo", "Natália", "Marcos", "Priscila", "Henrique", "Débora",
	"Vinícius", "Renata", "Leonardo", "Tatiane", "Fábio", "Bianca", "Matheus",
	"Aline", "Igor", "Cláudia", "Alexandre", "Luana", "Roberto", "Raquel",
}

var lastNames = []string{
	"Silva", "Santos", "Oliveira", "Souza", "Lima", "Pereira", "Ferreira",
	"Costa", "Rodrigues", "Almeida", "Nascimento", "Araújo", "Carvalho",
	"Gomes", "Martins", "Ribeiro", "Rocha", "Alves", "Monteiro", "Mendes",
	"Barros", "Freitas", "Barbosa", "Pinto", "Cardoso", "Campos", "Teixeira",
	"Cavalcanti", "Dias", "Vieira", "Moreira", "Nunes", "Lopes", "Ramos",
	"Correia", "Andrade", "Azevedo", "Fernandes", "Marques", "Cruz",
}

var groupThemes = []string{
	"Clientes", "Fornecedores", "Amigos", "Família", "Trabalho",
	"Faculdade", "Academia", "Vizinhos", "Condomínio", "Time de Futebol",
	"Igreja", "Escola dos Filhos", "Viagem", "Churrasco", "Aniversariantes",
}

func main() {
	dbPath := flag.String("db", "store/zapcast.db", "SQLite database path")
	nc := flag.Int("contacts", 1000, "number of contacts to create")
	ng := flag.Int("groups", 100, "number of groups to create")
	reset := flag.Bool("reset", false, "delete existing contacts/groups/broadcasts before seeding")
	seedVal := flag.Int64("seed", 42, "RNG seed for reproducible data")
	flag.Parse()

	if *nc < 0 || *ng < 0 {
		log.Fatal("contacts and groups must be non-negative")
	}

	rng := rand.New(rand.NewSource(*seedVal))
	ctx := context.Background()

	// Discard logger — seed runs as a CLI; structured noise to stdout would
	// drown the human-readable progress lines below.
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := server.NewContactStore(ctx, lg, *dbPath)
	if err != nil {
		log.Fatalf("open contact store %q: %v", *dbPath, err)
	}
	defer func() { _ = store.Close() }()

	if *reset {
		if err := store.WipeAll(ctx); err != nil {
			log.Fatalf("reset: %v", err)
		}
		fmt.Println("reset: contacts, groups, memberships, broadcasts cleared")
	}

	phones, err := seedContacts(ctx, store, rng, *nc)
	if err != nil {
		log.Fatalf("seed contacts: %v", err)
	}

	groupMembers, err := seedGroups(ctx, store, rng, *ng, phones)
	if err != nil {
		log.Fatalf("seed groups: %v", err)
	}

	fmt.Printf("seed done: +%d contacts, +%d groups, +%d memberships\n", len(phones), *ng, groupMembers)

	// Final tallies so the operator can sanity-check the result.
	contacts, _ := store.GetContacts(ctx)
	groups, _ := store.GetGroups(ctx)
	fmt.Printf("db totals:  %d contacts,  %d groups\n", len(contacts), len(groups))
}

func seedContacts(ctx context.Context, store *server.ContactStore, rng *rand.Rand, count int) ([]string, error) {
	phones := make([]string, 0, count)
	for i := 0; i < count; i++ {
		name := firstNames[rng.Intn(len(firstNames))] + " " + lastNames[rng.Intn(len(lastNames))]
		// 55 + 11 DDD + 9-digit mobile starting at 900000000. Range supports
		// ~100M unique contacts — well above any realistic seed size.
		phone := fmt.Sprintf("5511%09d", 900000000+i)
		if _, err := store.AddContact(ctx, name, phone, nil); err != nil {
			// Duplicate phones (rerun without -reset) are expected — keep
			// going so multi-run flows still produce usable group counts.
			continue
		}
		phones = append(phones, phone)
	}
	return phones, nil
}

func seedGroups(ctx context.Context, store *server.ContactStore, rng *rand.Rand, count int, phones []string) (int, error) {
	memberships := 0
	for i := 0; i < count; i++ {
		theme := groupThemes[rng.Intn(len(groupThemes))]
		gname := fmt.Sprintf("%s %d", theme, i+1)

		// Each group pulls 0–5 distinct random members from the contact pool.
		k := rng.Intn(6)
		members := make([]string, 0, k)
		picked := make(map[string]bool, k)
		for j := 0; j < k && len(phones) > 0; j++ {
			p := phones[rng.Intn(len(phones))]
			if picked[p] {
				continue
			}
			picked[p] = true
			members = append(members, p)
		}

		if _, err := store.AddGroup(ctx, gname, members); err != nil {
			// Same idempotency rationale as AddContact above.
			continue
		}
		memberships += len(members)
	}
	return memberships, nil
}
