package httpapi_test

import (
	"net/http"
	"strconv"
	"testing"
)

// TestRevokedSessionsStopServingRequests revokes every session of one safety
// operator from the administrator endpoint and checks that the already issued
// bearer tokens of that operator stop being accepted right away, while the
// administrator session and a fresh login keep working.
func TestRevokedSessionsStopServingRequests(t *testing.T) {
	c := newClient(t)
	operatorID := c.actors.Operator.OperatorID

	warm := c.do(http.MethodGet, "/api/v1/auth/me", c.actors.OperatorToken, nil, nil)
	if warm.status != http.StatusOK {
		t.Fatalf("the seeded operator session must work before the revocation: %+v", warm)
	}
	if int64(warm.body["operator_id"].(float64)) != operatorID {
		t.Fatalf("unexpected operator behind the seeded token: %+v", warm.body)
	}

	secondLogin := c.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "safety-lin",
		"password": "driver-secret-1",
	}, nil)
	if secondLogin.status != http.StatusOK {
		t.Fatalf("the second login of the operator failed: %+v", secondLogin)
	}
	secondToken, ok := secondLogin.body["token"].(string)
	if !ok || secondToken == "" {
		t.Fatalf("the second login must return a token: %+v", secondLogin.body)
	}
	secondWarm := c.do(http.MethodGet, "/api/v1/auth/me", secondToken, nil, nil)
	if secondWarm.status != http.StatusOK {
		t.Fatalf("the second operator session must work before the revocation: %+v", secondWarm)
	}

	revoke := c.do(http.MethodPost,
		"/api/v1/operators/"+strconv.FormatInt(operatorID, 10)+"/session-revocations",
		c.actors.AdminToken, nil, nil)
	if revoke.status != http.StatusOK {
		t.Fatalf("the administrator must be able to revoke the sessions: %+v", revoke)
	}
	if revoke.body["revoked"].(float64) < 2 {
		t.Fatalf("both operator sessions must be reported as revoked, got %v", revoke.body["revoked"])
	}

	for name, token := range map[string]string{
		"seeded": c.actors.OperatorToken,
		"second": secondToken,
	} {
		after := c.do(http.MethodGet, "/api/v1/auth/me", token, nil, nil)
		if after.status != http.StatusUnauthorized {
			t.Fatalf("the %s session must stop working after the revocation, got %d: %+v",
				name, after.status, after.body)
		}
		errorBody, present := after.body["error"].(map[string]any)
		if !present {
			t.Fatalf("the %s session must be refused with the error envelope: %+v", name, after.body)
		}
		if errorBody["code"] != "session_revoked" {
			t.Fatalf("the %s session must be refused as revoked, got %v", name, errorBody["code"])
		}
		listing := c.do(http.MethodGet, "/api/v1/assignments", token, nil, nil)
		if listing.status != http.StatusUnauthorized {
			t.Fatalf("the %s session must not reach business endpoints after the revocation, got %d",
				name, listing.status)
		}
	}

	admin := c.do(http.MethodGet, "/api/v1/auth/me", c.actors.AdminToken, nil, nil)
	if admin.status != http.StatusOK {
		t.Fatalf("the administrator session must stay valid: %+v", admin)
	}
	if int64(admin.body["operator_id"].(float64)) != c.actors.Admin.OperatorID {
		t.Fatalf("unexpected operator behind the administrator token: %+v", admin.body)
	}

	relogin := c.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "safety-lin",
		"password": "driver-secret-1",
	}, nil)
	if relogin.status != http.StatusOK {
		t.Fatalf("the operator must be able to log in again: %+v", relogin)
	}
	freshToken, ok := relogin.body["token"].(string)
	if !ok || freshToken == "" {
		t.Fatalf("the fresh login must return a token: %+v", relogin.body)
	}
	fresh := c.do(http.MethodGet, "/api/v1/auth/me", freshToken, nil, nil)
	if fresh.status != http.StatusOK {
		t.Fatalf("the fresh operator session must work: %+v", fresh)
	}
	if int64(fresh.body["operator_id"].(float64)) != operatorID {
		t.Fatalf("unexpected operator behind the fresh token: %+v", fresh.body)
	}
}
