package central

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
)

// The score-over-time chart (VERSION-1-1.md §6): one stepped line per top-10
// participant, labelled at its end with the name and score, so a line is
// identified by its label and position and not by its colour alone.

type chartLine struct {
	Points string // the polyline's points attribute
	Index  int    // colour: .s0 … .s9 in chart.svg
	Label  string
	LX, LY float64
	Score  int
}

type chartTick struct {
	Pos   float64
	Label string
}

type chartModel struct {
	W, H                   int
	Left, Right, Top, Base float64
	Lines                  []chartLine
	XTicks, YTicks         []chartTick
	Empty                  bool
	Summary                string
}

const (
	chartW, chartH        = 960, 320
	chartLeft, chartRight = 48.0, 760.0 // the labels go right of the plot
	chartTop, chartBase   = 16.0, 290.0
	labelGap              = 14.0
	chartLabelMax         = 22
)

func f1(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

func niceMax(v int) int {
	if v <= 0 {
		return 100
	}
	step := math.Pow(10, math.Floor(math.Log10(float64(v))))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if float64(v) <= m*step {
			return int(m * step)
		}
	}
	return v
}

func buildChart(b *store.Board, loc *time.Location) chartModel {
	m := chartModel{W: chartW, H: chartH, Left: chartLeft, Right: chartRight, Top: chartTop, Base: chartBase}
	maxScore := 0
	for _, s := range b.Series {
		if n := len(s.Points); n > 0 {
			maxScore = max(maxScore, s.Points[n-1].Score)
		}
	}
	if len(b.Series) == 0 || maxScore == 0 {
		m.Empty = true
		m.Summary = "Score over time: no points scored yet."
		return m
	}
	top := niceMax(maxScore)
	span := b.To.Sub(b.From)
	if span <= 0 {
		span = time.Minute
	}
	x := func(t time.Time) float64 {
		f := float64(t.Sub(b.From)) / float64(span)
		return chartLeft + math.Max(0, math.Min(1, f))*(chartRight-chartLeft)
	}
	y := func(score int) float64 { return chartBase - float64(score)/float64(top)*(chartBase-chartTop) }

	var sum []string
	for i, s := range b.Series {
		var pts []string
		prev := 0
		for j, p := range s.Points {
			if j > 0 { // a step: along at the old score, then up
				pts = append(pts, f1(x(p.At))+","+f1(y(prev)))
			}
			pts = append(pts, f1(x(p.At))+","+f1(y(p.Score)))
			prev = p.Score
		}
		pts = append(pts, f1(x(b.To))+","+f1(y(prev)))
		label := s.Name
		if r := []rune(label); len(r) > chartLabelMax {
			label = string(r[:chartLabelMax-1]) + "…"
		}
		m.Lines = append(m.Lines, chartLine{Points: strings.Join(pts, " "), Index: i % 10, Label: label, LX: chartRight + 8, LY: y(prev) + 4, Score: prev})
		sum = append(sum, fmt.Sprintf("%s %d", s.Name, prev))
	}
	// Labels may not overlap: sort by height and push each one down until
	// it is labelGap below the previous, then pull the column up if it ran
	// off the bottom.
	idx := make([]int, len(m.Lines))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return m.Lines[idx[a]].LY < m.Lines[idx[b]].LY })
	for k := 1; k < len(idx); k++ {
		if prev, cur := &m.Lines[idx[k-1]], &m.Lines[idx[k]]; cur.LY < prev.LY+labelGap {
			cur.LY = prev.LY + labelGap
		}
	}
	if n := len(idx); n > 0 {
		if over := m.Lines[idx[n-1]].LY - (chartH - 4); over > 0 {
			for _, i := range idx {
				m.Lines[i].LY -= over
			}
		}
	}
	for _, v := range []int{0, top / 2, top} {
		m.YTicks = append(m.YTicks, chartTick{Pos: y(v), Label: strconv.Itoa(v)})
	}
	for k := 0; k <= 4; k++ {
		t := b.From.Add(span * time.Duration(k) / 4)
		m.XTicks = append(m.XTicks, chartTick{Pos: x(t), Label: t.In(loc).Format("15:04")})
	}
	m.Summary = "Score over time, top " + strconv.Itoa(len(b.Series)) + ": " + strings.Join(sum, ", ") + "."
	return m
}
