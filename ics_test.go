package main

import "testing"

func TestClassroomTag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"一教503(白云)", "一教503"},
		{"一教327（白云校区）", "一教327"},
		{"教学科研楼311（白云）", "教学科研楼311"},
		{"匹克球场1（白云）", "匹克球场1"},
		{"一教 503", "一教503"},
		{"白云校区二教302", "二教302"},
		{"体育馆A305", "体育馆A305"},
		{"503", "503"},
		{"", ""},
		{"线上", ""},
		{"无楼号", ""},
	}
	for _, c := range cases {
		if got := classroomTag(c.in); got != c.want {
			t.Errorf("classroomTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassroomTagOf(t *testing.T) {
	cases := []struct {
		k    kbItem
		want string
	}{
		{kbItem{CDMC: "一教503(白云)", LH: "第一教学楼（白云校区）"}, "一教503"},
		{kbItem{CDMC: "503", LH: "第一教学楼（白云校区）"}, "第一教学楼503"},
		{kbItem{CDMC: "", LH: "第一教学楼（白云校区）"}, ""},
	}
	for _, c := range cases {
		if got := classroomTagOf(c.k); got != c.want {
			t.Errorf("classroomTagOf(%+v) = %q, want %q", c.k, got, c.want)
		}
	}
}
