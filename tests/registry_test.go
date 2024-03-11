package tests

import (
	"testing"

	"github.com/duglin/xreg-github/registry"
)

func TestCreateRegistry(t *testing.T) {
	reg := NewRegistry("TestCreateRegistry")
	defer PassDeleteReg(t, reg)
	xCheck(t, reg != nil, "reg shouldn't be nil")

	// Check basic GET first
	xCheckGet(t, reg, "/",
		`{
  "specversion": "0.5",
  "id": "TestCreateRegistry",
  "epoch": 1,
  "self": "http://localhost:8181/"
}
`)
	xCheckGet(t, reg, "/xxx", "Unknown Group type: xxx\n")
	xCheckGet(t, reg, "xxx", "Unknown Group type: xxx\n")
	xCheckGet(t, reg, "/xxx/yyy", "Unknown Group type: xxx\n")
	xCheckGet(t, reg, "xxx/yyy", "Unknown Group type: xxx\n")

	// make sure dups generate an error
	reg2, err := registry.NewRegistry(nil, "TestCreateRegistry")
	defer reg2.Rollback()
	if err == nil || reg2 != nil {
		t.Errorf("Creating same named registry worked!")
	}

	// make sure it was really created
	reg3, err := registry.FindRegistry(nil, "TestCreateRegistry")
	defer reg3.Rollback()
	xCheck(t, err == nil && reg3 != nil,
		"Finding TestCreateRegistry should have worked")

	reg3, err = registry.NewRegistry(nil, "")
	defer PassDeleteReg(t, reg3)
	xNoErr(t, err)
	xCheck(t, reg3 != nil, "reg3 shouldn't be nil")
	xCheck(t, reg3 != reg, "reg3 should be different from reg")

	xCheckGet(t, reg, "", `{
  "specversion": "0.5",
  "id": "TestCreateRegistry",
  "epoch": 1,
  "self": "http://localhost:8181/"
}
`)
}

func TestDeleteRegistry(t *testing.T) {
	reg, err := registry.NewRegistry(nil, "TestDeleteRegistry")
	defer reg.Rollback()
	xNoErr(t, err)
	xCheck(t, reg != nil, "reg shouldn't be nil")

	err = reg.Delete()
	xNoErr(t, err)
	reg.Commit()

	reg, err = registry.FindRegistry(nil, "TestDeleteRegistry")
	defer reg.Rollback()
	xCheck(t, reg == nil && err == nil,
		"Finding TestCreateRegistry found one but shouldn't")
}

func TestRefreshRegistry(t *testing.T) {
	reg := NewRegistry("TestRefreshRegistry")
	defer PassDeleteReg(t, reg)

	reg.Entity.Object["xxx"] = "yyy"
	xCheck(t, reg.Get("xxx") == "yyy", "xxx should be yyy")

	err := reg.Refresh()
	xNoErr(t, err)

	xCheck(t, reg.Get("xxx") == nil, "xxx should not be there")
}

func TestFindRegistry(t *testing.T) {
	reg, err := registry.FindRegistry(nil, "TestFindRegistry")
	defer reg.Rollback()
	xCheck(t, reg == nil && err == nil,
		"Shouldn't have found TestFindRegistry")

	reg, err = registry.NewRegistry(nil, "TestFindRegistry")
	defer reg.Commit()
	defer reg.Delete() // PassDeleteReg(t, reg)
	xNoErr(t, err)

	reg2, err := registry.FindRegistry(nil, reg.UID)
	defer reg2.Rollback()
	xNoErr(t, err)
	xJSONCheck(t, reg2, reg)
}

func TestRegistryProps(t *testing.T) {
	reg := NewRegistry("TestRegistryProps")
	defer PassDeleteReg(t, reg)

	err := reg.Set("specversion", "x.y")
	if err == nil {
		t.Errorf("Setting specversion to x.y should have failed")
		t.FailNow()
	}
	reg.Set("name", "nameIt")
	reg.Set("description", "a very cool reg")
	reg.Set("documentation", "https://docs.com")
	reg.Set("labels.stage", "dev")

	xCheckGet(t, reg, "", `{
  "specversion": "0.5",
  "id": "TestRegistryProps",
  "name": "nameIt",
  "epoch": 1,
  "self": "http://localhost:8181/",
  "description": "a very cool reg",
  "documentation": "https://docs.com",
  "labels": {
    "stage": "dev"
  }
}
`)
}

func TestRegistryRequiredFields(t *testing.T) {
	reg := NewRegistry("TestRegistryRequiredFields")
	defer PassDeleteReg(t, reg)

	_, err := reg.Model.AddAttribute(&registry.Attribute{
		Name:           "clireq",
		Type:           registry.STRING,
		ClientRequired: true,
		ServerRequired: true,
	})
	xNoErr(t, err)

	// Commit before we call Set below otherwise the Tx will be rolled back
	reg.Commit()

	err = reg.Set("description", "testing")
	xCheckErr(t, err, "Required property \"clireq\" is missing")

	xNoErr(t, reg.JustSet("clireq", "testing2"))
	xNoErr(t, reg.Set("description", "testing"))

	xHTTP(t, reg, "GET", "/", "", 200, `{
  "specversion": "0.5",
  "id": "TestRegistryRequiredFields",
  "epoch": 1,
  "self": "http://localhost:8181/",
  "description": "testing",
  "clireq": "testing2"
}
`)

}
