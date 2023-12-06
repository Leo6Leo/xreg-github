package tests

import (
	"fmt"
	"testing"
)

func TestSetResource(t *testing.T) {
	reg := NewRegistry("TestSetResource")
	defer PassDeleteReg(t, reg)

	gm, _ := reg.Model.AddGroupModel("dirs", "dir")
	gm.AddResourceModel("files", "file", 0, true, true, true)

	dir, _ := reg.AddGroup("dirs", "d1")
	file, _ := dir.AddResource("files", "f1", "v1")

	// /dirs/d1/f1/v1

	// Make sure setting it on the version is seen by res.Latest and res
	namePP := NewPP().P("name").UI()
	file.Set(namePP, "myName")
	ver, _ := file.FindVersion("v1")
	val := ver.Get(namePP)
	if val != "myName" {
		t.Errorf("ver.Name is %q, should be 'myName'", val)
	}

	name := file.Get(namePP).(string)
	xCheckEqual(t, "", name, "myName")

	// Verify that nil and "" are treated differently
	ver.Set(namePP, nil)
	ver2, _ := file.FindVersion(ver.UID)
	xJSONCheck(t, ver2, ver)
	val = ver.Get(namePP)
	xCheck(t, val == nil, "Setting to nil should return nil")

	ver.Set(namePP, "")
	ver2, _ = file.FindVersion(ver.UID)
	xJSONCheck(t, ver2, ver)
	val = ver.Get(namePP)
	xCheck(t, val == "", "Setting to '' should return ''")
}

func TestSetVersion(t *testing.T) {
	reg := NewRegistry("TestSetVersion")
	defer PassDeleteReg(t, reg)

	gm, _ := reg.Model.AddGroupModel("dirs", "dir")
	gm.AddResourceModel("files", "file", 0, true, true, true)

	dir, _ := reg.AddGroup("dirs", "d1")
	file, _ := dir.AddResource("files", "f1", "v1")
	ver, _ := file.FindVersion("v1")

	// /dirs/d1/f1/v1

	// Make sure setting it on the version is seen by res.Latest and res
	namePP := NewPP().P("name").UI()
	ver.Set(namePP, "myName")
	file, _ = dir.FindResource("files", "f1")
	l, err := file.GetLatest()
	xNoErr(t, err)
	xCheck(t, l != nil, "latest is nil")
	val := l.Get(namePP)
	if val != "myName" {
		t.Errorf("resource.latest.Name is %q, should be 'myName'", val)
	}
	val = file.Get(namePP)
	if val != "myName" {
		t.Errorf("resource.Name is %q, should be 'myName'", val)
	}

	// Make sure we can also still see it from the version itself
	ver, _ = file.FindVersion("v1")
	val = ver.Get(namePP)
	if val != "myName" {
		t.Errorf("version.Name is %q, should be 'myName'", val)
	}
}

func TestSetDots(t *testing.T) {
	reg := NewRegistry("TestSetDots")
	defer PassDeleteReg(t, reg)

	gm, _ := reg.Model.AddGroupModel("dirs", "dir")
	gm.AddResourceModel("files", "file", 0, true, true, true)

	// check some dots in the prop names - and some labels stuff too
	dir, _ := reg.AddGroup("dirs", "d1")
	labels := NewPP().P("labels")

	err := dir.Set(labels.UI(), "xxx")
	xCheck(t, err != nil, "labels=xxx should fail")

	// Nesting under labels should fail
	err = dir.Set(labels.P("xxx").P("yyy").UI(), "xy")
	xJSONCheck(t, err, "Traversing into a map/scalar \"labels\": xxx.yyy")

	// dots are ok as tag names
	err = dir.Set(labels.P("abc.def").UI(), "ABC")
	xNoErr(t, err)
	xJSONCheck(t, dir.Get(labels.P("abc.def").UI()), "ABC")

	dir.Refresh()

	xCheckGet(t, reg, "/dirs/d1", `{
  "id": "d1",
  "epoch": 1,
  "self": "http://localhost:8181/dirs/d1",
  "labels": {
    "abc.def": "ABC"
  },

  "filesCount": 0,
  "filesUrl": "http://localhost:8181/dirs/d1/files"
}
`)

	err = dir.Set("labels", nil)
	xCheck(t, err.Error() == "Invalid property name: labels",
		fmt.Sprintf("labels=nil should fail: %s", err))

	err = dir.Set(NewPP().P("labels").P("xxx/yyy").UI(), nil)
	xCheck(t, err.Error() == `Unexpected / in "labels.xxx/yyy" at pos 11`,
		fmt.Sprintf("labels.xxx/yyy=nil should fail: %s", err))

	err = dir.Set(NewPP().P("labels").P("").P("abc").UI(), nil)
	xJSONCheck(t, err, `Unexpected . in "labels..abc" at pos 8`)

	err = dir.Set(NewPP().P("labels").P("xxx.yyy").UI(), "xxx")
	xJSONCheck(t, err, nil)

	err = dir.Set(NewPP().P("xxx.yyy").UI(), nil)
	xJSONCheck(t, err, `Can't find attribute "xxx.yyy"`)
	xCheck(t, err != nil, "xxx.yyy=nil should fail")
	err = dir.Set("xxx.", "xxx")
	xCheck(t, err != nil, "xxx.=xxx should fail")
	err = dir.Set(".xxx", "xxx")
	xCheck(t, err != nil, ".xxx=xxx should fail")
	err = dir.Set(".xxx.", "xxx")
	xCheck(t, err != nil, ".xxx.=xxx should fail")
}

func TestSetLabels(t *testing.T) {
	reg := NewRegistry("TestSetLabels")
	defer PassDeleteReg(t, reg)

	gm, _ := reg.Model.AddGroupModel("dirs", "dir")
	gm.AddResourceModel("files", "file", 0, true, true, true)

	dir, _ := reg.AddGroup("dirs", "d1")
	file, _ := dir.AddResource("files", "f1", "v1")
	ver, _ := file.FindVersion("v1")
	ver2, _ := file.AddVersion("v2")

	// /dirs/d1/f1/v1
	labels := NewPP().P("labels")
	err := reg.Set(labels.P("r2").UI(), "123.234") // OLD: notice it's not a string
	xNoErr(t, err)
	reg.Refresh()
	// But it's a string here because labels is a map[string]string
	xJSONCheck(t, reg.Get(labels.P("r2").UI()), "123.234")
	err = reg.Set("labels.r1", "foo")
	xNoErr(t, err)
	reg.Refresh()
	xJSONCheck(t, reg.Get(labels.P("r1").UI()), "foo")
	err = reg.Set(labels.P("r1").UI(), nil)
	xNoErr(t, err)
	reg.Refresh()
	xJSONCheck(t, reg.Get(labels.P("r1").UI()), nil)

	err = dir.Set(labels.P("d1").UI(), "bar")
	xNoErr(t, err)
	dir.Refresh()
	xJSONCheck(t, dir.Get(labels.P("d1").UI()), "bar")
	// test override
	err = dir.Set(labels.P("d1").UI(), "foo")
	xNoErr(t, err)
	dir.Refresh()
	xJSONCheck(t, dir.Get(labels.P("d1").UI()), "foo")
	err = dir.Set(labels.P("d1").UI(), nil)
	xNoErr(t, err)
	dir.Refresh()
	xJSONCheck(t, dir.Get(labels.P("d1").UI()), nil)

	err = file.Set(labels.P("f1").UI(), "foo")
	xNoErr(t, err)
	file.Refresh()
	xJSONCheck(t, file.Get(labels.P("f1").UI()), "foo")
	err = file.Set(labels.P("f1").UI(), nil)
	xNoErr(t, err)
	file.Refresh()
	xJSONCheck(t, file.Get(labels.P("f1").UI()), nil)

	err = ver.Set(labels.P("v1").UI(), "foo")
	xNoErr(t, err)
	ver.Refresh()
	xJSONCheck(t, ver.Get(labels.P("v1").UI()), "foo")
	err = ver.Set(labels.P("v1").UI(), nil)
	xNoErr(t, err)
	ver.Refresh()
	xJSONCheck(t, ver.Get(labels.P("v1").UI()), nil)

	dir.Set(labels.P("dd").UI(), "dd.foo")
	file.Set(labels.P("ff").UI(), "ff.bar")
	// ver.Set(labels.P("vv").UI(), 987.234)
	ver.Set(labels.P("vv2").UI(), "v11")
	ver2.Set(labels.P("2nd").UI(), "3rd")
	// ver2.Set(labels.P("bool1").UI(), true)
	// ver2.Set(labels.P("bool2").UI(), false)

	xCheckGet(t, reg, "?inline", `{
  "specVersion": "0.5",
  "id": "TestSetLabels",
  "epoch": 1,
  "self": "http://localhost:8181/",
  "labels": {
    "r2": "123.234"
  },

  "dirs": {
    "d1": {
      "id": "d1",
      "epoch": 1,
      "self": "http://localhost:8181/dirs/d1",
      "labels": {
        "dd": "dd.foo"
      },

      "files": {
        "f1": {
          "id": "f1",
          "epoch": 1,
          "self": "http://localhost:8181/dirs/d1/files/f1",
          "latestVersionId": "v2",
          "latestVersionUrl": "http://localhost:8181/dirs/d1/files/f1/versions/v2",
          "labels": {
            "2nd": "3rd",
            "ff": "ff.bar"
          },

          "versions": {
            "v1": {
              "id": "v1",
              "epoch": 1,
              "self": "http://localhost:8181/dirs/d1/files/f1/versions/v1",
              "labels": {
                "vv2": "v11"
              }
            },
            "v2": {
              "id": "v2",
              "epoch": 1,
              "self": "http://localhost:8181/dirs/d1/files/f1/versions/v2",
              "latest": true,
              "labels": {
                "2nd": "3rd",
                "ff": "ff.bar"
              }
            }
          },
          "versionsCount": 2,
          "versionsUrl": "http://localhost:8181/dirs/d1/files/f1/versions"
        }
      },
      "filesCount": 1,
      "filesUrl": "http://localhost:8181/dirs/d1/files"
    }
  },
  "dirsCount": 1,
  "dirsUrl": "http://localhost:8181/dirs"
}
`)

	file.SetLatest(ver)
	xCheckGet(t, reg, "?inline", `{
  "specVersion": "0.5",
  "id": "TestSetLabels",
  "epoch": 1,
  "self": "http://localhost:8181/",
  "labels": {
    "r2": "123.234"
  },

  "dirs": {
    "d1": {
      "id": "d1",
      "epoch": 1,
      "self": "http://localhost:8181/dirs/d1",
      "labels": {
        "dd": "dd.foo"
      },

      "files": {
        "f1": {
          "id": "f1",
          "epoch": 1,
          "self": "http://localhost:8181/dirs/d1/files/f1",
          "latestVersionId": "v1",
          "latestVersionUrl": "http://localhost:8181/dirs/d1/files/f1/versions/v1",
          "labels": {
            "vv2": "v11"
          },

          "versions": {
            "v1": {
              "id": "v1",
              "epoch": 1,
              "self": "http://localhost:8181/dirs/d1/files/f1/versions/v1",
              "latest": true,
              "labels": {
                "vv2": "v11"
              }
            },
            "v2": {
              "id": "v2",
              "epoch": 1,
              "self": "http://localhost:8181/dirs/d1/files/f1/versions/v2",
              "labels": {
                "2nd": "3rd",
                "ff": "ff.bar"
              }
            }
          },
          "versionsCount": 2,
          "versionsUrl": "http://localhost:8181/dirs/d1/files/f1/versions"
        }
      },
      "filesCount": 1,
      "filesUrl": "http://localhost:8181/dirs/d1/files"
    }
  },
  "dirsCount": 1,
  "dirsUrl": "http://localhost:8181/dirs"
}
`)
}
