package pokies

import (
	"encoding/json"
	"errors"
)

// symbol is a single-width ASCII slot face.
type symbol byte

const (
	symBlank  symbol = '-'
	sym7      symbol = '7'
	symDollar symbol = '$'
	symStar   symbol = '*'
	symBar    symbol = 'B'
	symCherry symbol = 'C'
)

// stripOrder is the stable symbol order used to lay out the weighted strip, so
// a seeded room reproduces outcomes for a given variant.
var stripOrder = []symbol{sym7, symDollar, symStar, symBar, symCherry}

var symbolByName = map[string]symbol{
	"7": sym7, "$": symDollar, "*": symStar, "B": symBar, "C": symCherry,
}

// oddsVariant is the on-the-wire JSON document under the "odds-variant" config
// key (the same document the arcade admin area writes for native pokies).
type oddsVariant struct {
	Name     string         `json:"name"`
	Weights  map[string]int `json:"weights"`
	Paytable []payEntry     `json:"paytable"`
}

type payEntry struct {
	Faces      string `json:"faces"`
	Multiplier int    `json:"multiplier"`
}

// variant is the compiled runtime form: an ordered weighted strip plus a
// three-of-a-kind paytable.
type variant struct {
	name    string
	strip   []symbol
	triples map[symbol]int
}

func (v *variant) payout(reels [3]symbol) int {
	if reels[0] == reels[1] && reels[1] == reels[2] {
		return v.triples[reels[0]]
	}
	return 0
}

func compileVariant(doc oddsVariant) (*variant, error) {
	v := &variant{name: doc.Name, triples: map[symbol]int{}}
	for _, s := range stripOrder {
		w := doc.Weights[string(rune(s))]
		for i := 0; i < w; i++ {
			v.strip = append(v.strip, s)
		}
	}
	if len(v.strip) == 0 {
		return nil, errors.New("pokies: variant has an empty strip")
	}
	for _, e := range doc.Paytable {
		s, ok := symbolByName[e.Faces]
		if !ok || e.Multiplier < 0 {
			return nil, errors.New("pokies: bad paytable entry")
		}
		v.triples[s] = e.Multiplier
	}
	return v, nil
}

func parseVariant(blob []byte) (*variant, error) {
	var doc oddsVariant
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, err
	}
	return compileVariant(doc)
}

// defaultVariant is the compiled-in tuning: strip weights 7:1 $:2 *:3 B:5 C:7
// and paytable 500/150/55/10 (cherries pay nothing), matching native pokies.
func defaultVariant() *variant {
	v, err := compileVariant(oddsVariant{
		Name:    "default",
		Weights: map[string]int{"7": 1, "$": 2, "*": 3, "B": 5, "C": 7},
		Paytable: []payEntry{
			{Faces: "7", Multiplier: 500},
			{Faces: "$", Multiplier: 150},
			{Faces: "*", Multiplier: 55},
			{Faces: "B", Multiplier: 10},
		},
	})
	if err != nil {
		panic(err)
	}
	return v
}

// windowAt returns the three visible faces when the strip is stopped with idx
// centered (wrapping).
func windowAt(strip []symbol, idx int) [3]symbol {
	n := len(strip)
	return [3]symbol{strip[(idx-1+n)%n], strip[idx], strip[(idx+1)%n]}
}

// rollWindow returns the visible faces for a reel still spinning, scrolled to
// the given animation offset (contiguous, so the wheel appears to roll).
func rollWindow(strip []symbol, offset int) [3]symbol {
	n := len(strip)
	o := ((offset % n) + n) % n
	return [3]symbol{strip[o], strip[(o+1)%n], strip[(o+2)%n]}
}
