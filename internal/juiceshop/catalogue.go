// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package juiceshop

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

// The challenge catalogue of the pinned Juice Shop, cut down from
// testdata/challenges-fresh.json to what scoring needs (key, name, category,
// difficulty, disabledEnv, hasCodingChallenge). Captured again with the
// fixtures when Juice Shop is upgraded (testdata/README.md):
//
//	python3 -c 'import json; d=json.load(open("testdata/challenges-fresh.json"))["data"]; \
//	  json.dump([{k: c[k] for k in ("id","key","name","category","difficulty","disabledEnv","hasCodingChallenge")} for c in d], \
//	  open("catalogue-20.3.0.json","w"), ensure_ascii=False, indent=0)'
//
// WHY the central server has its own copy: in 1.1 the participant token is on
// the student's VM, so anything a report says is the student's word. 1.0 took
// the catalogue from the reports; a doctored report could then have made every
// challenge worth six stars, for everybody. Now a report carries keys and
// times only, and what a key is worth is decided here.
//
//go:embed catalogue-20.3.0.json
var catalogueJSON []byte

// CatalogueVersion is the Juice Shop version the catalogue was captured from.
const CatalogueVersion = "20.3.0"

var (
	catOnce sync.Once
	catList []Challenge
	catMap  map[string]Challenge
)

func loadCatalogue() {
	if err := json.Unmarshal(catalogueJSON, &catList); err != nil {
		panic("juiceshop: embedded catalogue: " + err.Error())
	}
	sort.Slice(catList, func(i, j int) bool { return catList[i].ID < catList[j].ID })
	catMap = make(map[string]Challenge, len(catList))
	for _, c := range catList {
		catMap[c.Key] = c
	}
}

// Catalogue returns every challenge of the pinned version, by id. The slice
// is shared: do not modify it.
func Catalogue() []Challenge {
	catOnce.Do(loadCatalogue)
	return catList
}

// Lookup returns one challenge of the catalogue.
func Lookup(key string) (Challenge, bool) {
	catOnce.Do(loadCatalogue)
	c, ok := catMap[key]
	return c, ok
}

// Prerequisites maps a challenge to one that Juice Shop ALWAYS solves first
// or at the same moment, so a report with the first and without the second
// is not something Juice Shop produced.
//
// Kept to what Juice Shop's code guarantees, not to what is merely the usual
// order: a student told the URL by a classmate skips the "usual" step
// honestly. In routes/fileServer.ts every file behind the poison null byte
// (eastere.gg, package.json.bak, coupons_2013.md.bak, suspicious_errors.yml)
// solves nullByteChallenge in the same request.
var Prerequisites = map[string]string{
	"easterEggLevelOneChallenge":      "nullByteChallenge",
	"forgottenDevBackupChallenge":     "nullByteChallenge",
	"forgottenBackupChallenge":        "nullByteChallenge",
	"misplacedSignatureFileChallenge": "nullByteChallenge",
}
