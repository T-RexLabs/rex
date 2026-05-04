package runner

import (
	"hash/fnv"
	"strings"
)

// FriendlyName derives a human-readable slug from a run id (the
// canonical HLC string). Format: "<adjective>-<animal>" — same
// pattern Docker / Heroku use for container and dyno names. The
// derivation is deterministic, so the same run id always produces
// the same friendly name, and reverse lookup (slug → run id) is a
// linear scan over known runs without persistence.
//
// Why no persistence: HLC is the canonical, stable run identifier
// (it carries causal ordering and is the partition key in central-
// node Postgres). A friendly slug is purely a render concern —
// adding a "name" field to RunStartedEvent would invite drift
// (what if the user renames a run? what if two runs hash to the
// same slug at scale?). Treating it as a pure function of the run
// id keeps the data model small.
//
// Collision: 84 adjectives × 84 animals = 7056 unique slugs. For
// a single workspace's run history this is plenty; if a workspace
// ever exceeds that, the linear-scan resolver will report
// "ambiguous" and the user can fall back to the HLC.
func FriendlyName(runID string) string {
	if runID == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(runID))
	sum := h.Sum64()
	adj := friendlyAdjectives[sum%uint64(len(friendlyAdjectives))]
	noun := friendlyAnimals[(sum/uint64(len(friendlyAdjectives)))%uint64(len(friendlyAnimals))]
	return adj + "-" + noun
}

// IsFriendlyName reports whether s looks like a friendly slug
// (lowercase, hyphenated, two-token). Used by run-id resolution
// to decide between "treat as HLC prefix" and "scan and match
// friendly name".
func IsFriendlyName(s string) bool {
	if s == "" {
		return false
	}
	hyphen := strings.IndexByte(s, '-')
	if hyphen <= 0 || hyphen == len(s)-1 {
		return false
	}
	for _, r := range s {
		if r == '-' {
			continue
		}
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// friendlyAdjectives is the curated word list for the first slug
// half. Choices skew calm/positive — these surface in run lists
// and audit logs. Avoid words with any negative connotation, any
// political/religious/cultural baggage, and anything that could
// read as a slur in any language we ship to.
var friendlyAdjectives = []string{
	"ancient", "azure", "bold", "brave", "breezy", "bright", "brisk", "calm",
	"cheery", "clever", "cosmic", "cozy", "crimson", "crisp", "curious",
	"dapper", "dazzling", "deft", "earnest", "eager", "elegant", "feisty",
	"fluent", "fond", "fresh", "frosty", "gentle", "graceful", "grand",
	"hardy", "hearty", "humble", "jolly", "keen", "kind", "lively", "lucid",
	"lucky", "merry", "mighty", "mindful", "modest", "neat", "nimble",
	"noble", "patient", "peppy", "placid", "plucky", "polished", "polite",
	"prudent", "quaint", "quiet", "quirky", "radiant", "rapid", "rare",
	"refined", "regal", "robust", "rosy", "rugged", "savvy", "serene",
	"shiny", "sleek", "snappy", "snazzy", "spry", "stalwart", "steady",
	"sturdy", "sunny", "swift", "tender", "thrifty", "tidy", "tranquil",
	"trusty", "valiant", "vivid", "warm", "winsome", "witty", "zesty",
}

// friendlyAnimals is the curated noun list. Same care as the
// adjective list — animals that exist in real life, recognizable
// across cultures, no tabloid associations.
var friendlyAnimals = []string{
	"alpaca", "antelope", "badger", "beaver", "bison", "bobcat", "buffalo",
	"capybara", "caracal", "caribou", "chipmunk", "cougar", "coyote",
	"crane", "cricket", "deer", "dingo", "dolphin", "dove", "duck",
	"eagle", "elk", "ermine", "falcon", "ferret", "finch", "fox", "gazelle",
	"gecko", "gibbon", "giraffe", "goose", "grouse", "hare", "hawk",
	"hedgehog", "heron", "hippo", "ibex", "ibis", "jackal", "jaguar",
	"jay", "kingfisher", "koala", "kookaburra", "lemur", "leopard", "lion",
	"lynx", "magpie", "manatee", "marmot", "marten", "meerkat", "mongoose",
	"moose", "narwhal", "newt", "ocelot", "octopus", "okapi", "orca",
	"osprey", "otter", "panda", "panther", "pelican", "pheasant", "platypus",
	"polecat", "puffin", "quokka", "rabbit", "raccoon", "raven", "robin",
	"salmon", "seal", "sparrow", "stoat", "swan", "tapir", "tiger", "wolf",
	"zebra",
}
