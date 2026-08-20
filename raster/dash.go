package raster

import "math"

const (
	minDashPeriod = 0.1
	maxDashRuns   = 1 << 16
)

type dasher struct {
	dashes []float32
	total  float32
	phase  float32
	cur    int
	start  float32
	on     bool
	buf    []Point
}

func newDasher(s *Stroke, scale float32) *dasher {
	if len(s.Dash) == 0 {
		return nil
	}
	total := float32(0)
	solid := len(s.Dash)%2 == 0
	for i, d := range s.Dash {
		if d < 0 || d != d || math.IsInf(float64(d), 0) {
			return nil
		}
		total += d
		if i%2 == 1 && d != 0 {
			solid = false
		}
	}
	if solid || total <= 0 || total*scale < minDashPeriod {
		return nil
	}
	return &dasher{dashes: s.Dash, total: total, phase: s.DashPhase}
}

func (d *dasher) reset() {
	ds := d.phase
	if ds < 0 || ds != ds {
		if ds != ds {
			ds = 0
		} else {
			cycle := d.total * 2
			ds = float32(float64(ds) + float64(math.Ceil(float64(-ds/cycle))*float64(cycle)))
		}
	}
	cycle := d.total
	if len(d.dashes)%2 == 1 {
		cycle *= 2
	}
	ds = float32(float64(ds) - float64(math.Floor(float64(ds/cycle))*float64(cycle)))

	d.cur, d.start, d.on = 0, 0, true
	for ds > 0 {
		if ds > d.dashes[d.cur] {
			ds -= d.dashes[d.cur]
			d.next()
			d.start = 0
		} else {
			d.start = ds
			ds = 0
		}
	}
}

func (d *dasher) next() {
	d.cur++
	if d.cur >= len(d.dashes) {
		d.cur = 0
	}
	d.on = !d.on
}

func (d *dasher) run(v []vertexDist, closed bool, emit func(pts []Point, whole bool)) {
	n := len(v)
	if n < 2 {
		emit([]Point{{v[0].x, v[0].y}}, false)
		return
	}
	count := n - 1
	if closed {
		count = n
	}

	seg := d.buf[:0]
	runs := 0
	fromStart := d.on
	if d.on {
		seg = append(seg, Point{v[0].x, v[0].y})
	}
	for i := 0; i < count && runs < maxDashRuns; i++ {
		a := v[i]
		b := v[(i+1)%n]
		if a.dist <= 0 {
			continue
		}
		for rest := a.dist; rest > 0 && runs < maxDashRuns; {
			dashRest := d.dashes[d.cur] - d.start
			if rest > dashRest {
				rest -= dashRest
				d.next()
				d.start = 0
				p := Point{
					b.x - (b.x-a.x)*rest/a.dist,
					b.y - (b.y-a.y)*rest/a.dist,
				}
				if d.on {
					seg = append(seg[:0], p)
				} else {
					emit(append(seg, p), false)
					seg = seg[:0]
					runs++
				}
			} else {
				d.start += rest
				rest = 0
				if d.on {
					seg = append(seg, Point{b.x, b.y})
				}
			}
		}
	}
	if d.on && len(seg) > 0 {
		emit(seg, closed && runs == 0 && fromStart)
	}
	d.buf = seg[:0]
}
