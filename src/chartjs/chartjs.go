package chartjs

import (
	"container/list"
	"encoding/json"
	"github.com/esrrhs/go-engine/src/loggo"
)

const (
	Red    = "rgb(255, 99, 132)"
	Orange = "rgb(255, 159, 64)"
	Yellow = "rgb(255, 205, 86)"
	Green  = "rgb(75, 192, 192)"
	Blue   = "rgb(54, 162, 235)"
	Purple = "rgb(153, 102, 255)"
	Grey   = "rgb(201, 203, 207)"
)

type LineDataEle struct {
	x string
	y int
}

type LineData struct {
	label           string
	backgroundColor string
	borderColor     string
	ele             list.List
	fill            bool
	max             int
}

type LineDataExportSet struct {
	Label           string `json:"label"`
	BackgroundColor string `json:"backgroundColor"`
	BorderColor     string `json:"borderColor"`
	Data            []int  `json:"data"`
	Fill            bool   `json:"fill"`
}
type LineDataExport struct {
	Labels   []string            `json:"labels"`
	Datasets []LineDataExportSet `json:"datasets"`
}

func NewLineData(name string, backgroundColor string, borderColor string, fill bool, max int) *LineData {
	ld := LineData{}
	ld.label = name
	ld.backgroundColor = backgroundColor
	ld.borderColor = borderColor
	ld.fill = fill
	ld.max = max
	return &ld
}

func (ld *LineData) Add(x string, y int) {
	lde := LineDataEle{x, y}
	ld.ele.PushBack(lde)
	if ld.ele.Len() > ld.max {
		ld.ele.Remove(ld.ele.Front())
	}
}

func (ld *LineData) Export() string {
	lde := LineDataExport{}
	for e := ld.ele.Front(); e != nil; e = e.Next() {
		ldele := e.Value.(LineDataEle)
		lde.Labels = append(lde.Labels, ldele.x)
	}
	ldes := LineDataExportSet{}
	ldes.Label = ld.label
	ldes.BackgroundColor = ld.backgroundColor
	ldes.BorderColor = ld.borderColor
	ldes.Fill = ld.fill
	for e := ld.ele.Front(); e != nil; e = e.Next() {
		ldele := e.Value.(LineDataEle)
		ldes.Data = append(ldes.Data, ldele.y)
	}

	lde.Datasets = append(lde.Datasets, ldes)

	b, err := json.Marshal(&lde)
	if err != nil {
		loggo.Error("Export Marshal fail %s", err)
		return ""
	}
	return string(b)
}
