package apis_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/auth"
	"golang.org/x/oauth2"
)

// linkSuperuserOAuth2 precreates an ExternalAuth link between the superuser
// with the specified email and the given OAuth2 provider account.
func linkSuperuserOAuth2(t testing.TB, app *tests.TestApp, superuserEmail, provider, providerId string) {
	superuser, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, superuserEmail)
	if err != nil {
		t.Fatal(err)
	}

	ea := core.NewExternalAuth(app)
	ea.SetCollectionRef(superuser.Collection().Id)
	ea.SetRecordRef(superuser.Id)
	ea.SetProvider(provider)
	ea.SetProviderId(providerId)
	if err := app.Save(ea); err != nil {
		t.Fatal(err)
	}
}

// superusersOAuth2Setup enables the "test" OAuth2 provider for the
// _superusers collection with the specified external user data.
func superusersOAuth2Setup(t testing.TB, app *tests.TestApp, user *auth.AuthUser) {
	auth.Providers["test"] = func() auth.Provider {
		return &oauth2MockProvider{
			AuthUser: user,
			Token:    &oauth2.Token{AccessToken: "abc"},
		}
	}

	collection, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		t.Fatal(err)
	}

	collection.OAuth2.Enabled = true
	collection.OAuth2.Providers = []core.OAuth2ProviderConfig{{
		Name:         "test",
		ClientId:     "123",
		ClientSecret: "456",
	}}
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
}

// superuserAuthToken returns a valid auth token for the superuser with the
// specified email.
//
// Note: the token is minted from a throwaway app instance which works
// because every tests.NewTestApp() clones the same test data (same auth
// secret and token keys), so the token validates in any test app instance.
func superuserAuthToken(t testing.TB, app *tests.TestApp, email string) string {
	record, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, email)
	if err != nil {
		t.Fatal(err)
	}

	token, err := record.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

func TestRecordAuthWithOAuth2Superusers(t *testing.T) {
	t.Parallel()

	testApp, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	testToken := superuserAuthToken(t, testApp, "test@example.com")
	testApp.Cleanup()

	scenarios := []tests.ApiScenario{
		{
			Name:   "disabled OAuth2 auth",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Body: strings.NewReader(`{
				"provider":     "test",
				"code":         "123",
				"codeVerifier": "456",
				"redirectURL":  "https://example.com"
			}`),
			ExpectedStatus:  403,
			ExpectedContent: []string{`"data":{}`},
			ExpectedEvents:  map[string]int{"*": 0},
		},
		{
			Name:   "missing provider config",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Body: strings.NewReader(`{
				"provider": "missing",
				"code":     "123"
			}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				superusersOAuth2Setup(t, app, &auth.AuthUser{Id: "test_id"})
			},
			ExpectedStatus: 400,
			ExpectedContent: []string{
				`"data":{`,
				`"provider":`,
			},
			ExpectedEvents: map[string]int{"*": 0},
		},
		{
			Name:   "unmatched OAuth2 user (no existing superuser to link to)",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Body: strings.NewReader(`{
				"provider":    "test",
				"code":        "123",
				"redirectURL": "https://example.com"
			}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				superusersOAuth2Setup(t, app, &auth.AuthUser{Id: "unknown_id", Email: "unknown@example.com"})
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// ensure that no superuser was created
				var total int
				if err := app.DB().NewQuery("select count(*) from `" + core.CollectionNameSuperusers + "`").
					Row(&total); err != nil {
					t.Fatal(err)
				}
				if total != 4 {
					t.Fatalf("Expected 4 superusers, got %d", total)
				}

				// ensure that no external auth link was created
				var links int
				err := app.DB().NewQuery("select count(*) from _externalAuths where provider = {:p}").
					Bind(dbx.Params{"p": "test"}).Row(&links)
				if err != nil {
					t.Fatal(err)
				}
				if links != 0 {
					t.Fatalf("Expected no external auth links, got %d", links)
				}
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`"data":{}`},
			// the OAuth2 request hook is triggered, but the superusers
			// record creation is rejected by the submit handler
			ExpectedEvents: map[string]int{
				"*":                             0,
				"OnRecordAuthWithOAuth2Request": 1,
			},
		},
		{
			Name:   "existing superuser external auth link",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Body: strings.NewReader(`{
				"provider":    "test",
				"code":        "123",
				"redirectURL": "https://example.com"
			}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				superusersOAuth2Setup(t, app, &auth.AuthUser{Id: "test_id", Email: "test@example.com"})

				linkSuperuserOAuth2(t, app, "test@example.com", "test", "test_id")
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// ensure that no extra link was created
				var links int
				err := app.DB().NewQuery("select count(*) from _externalAuths where provider = {:p}").
					Bind(dbx.Params{"p": "test"}).Row(&links)
				if err != nil {
					t.Fatal(err)
				}
				if links != 1 {
					t.Fatalf("Expected 1 external auth link, got %d", links)
				}
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"record":{`,
				`"id":"sywbhecnh46rhm0"`,
				`"email":"test@example.com"`,
				`"token":"`,
				`"isNew":false`,
			},
			NotExpectedContent: []string{
				`"tokenKey"`,
				`"password"`,
			},
			ExpectedEvents: map[string]int{
				"*": 0,
				// ---
				"OnRecordAuthWithOAuth2Request": 1,
				"OnRecordAuthRequest":           1,
				"OnRecordEnrich":                1,
				// ---
				// the existing ExternalAuth link is reused, so the only
				// created model is the AuthOrigin of the login alert
				"OnModelCreate":              1,
				"OnModelCreateExecute":       1,
				"OnModelValidate":            1,
				"OnModelAfterCreateSuccess":  1,
				"OnRecordCreate":             1,
				"OnRecordCreateExecute":      1,
				"OnRecordValidate":           1,
				"OnRecordAfterCreateSuccess": 1,
				// ---
				"OnMailerRecordAuthAlertSend": 1,
				"OnMailerSend":                1,
			},
		},
		{
			// a superuser sign-in must never bind an unlinked provider to an
			// existing superuser account based only on a matching email,
			// otherwise anyone controlling a provider account with a
			// superuser email would get a superuser token
			Name:   "existing superuser matched by OAuth2 email (not allowed to link)",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Body: strings.NewReader(`{
				"provider":    "test",
				"code":        "123",
				"redirectURL": "https://example.com"
			}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				superusersOAuth2Setup(t, app, &auth.AuthUser{Id: "new_id", Email: "test2@example.com"})
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// ensure that no external auth link was created
				links, err := app.CountRecords("_externalAuths", dbx.HashExp{
					"provider":   "test",
					"providerId": "new_id",
				})
				if err != nil {
					t.Fatal(err)
				}
				if links != 0 {
					t.Fatalf("Expected no external auth links, got %d", links)
				}
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`"data":{}`},
			// the OAuth2 request hook is triggered, but there is no record to
			// resolve the sign-in to and the creation is rejected by the submit handler
			ExpectedEvents: map[string]int{
				"*":                             0,
				"OnRecordAuthWithOAuth2Request": 1,
			},
		},
		{
			// the provider email deliberately doesn't match the authenticated
			// superuser to prove that the authenticated record takes precedence
			Name:   "existing superuser can link a new provider when already authenticated",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Headers: map[string]string{
				// authenticated as the test@example.com superuser
				"Authorization": testToken,
			},
			Body: strings.NewReader(`{
				"provider":    "test",
				"code":        "123",
				"redirectURL": "https://example.com"
			}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				superusersOAuth2Setup(t, app, &auth.AuthUser{Id: "new_id", Email: "other@example.com"})
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// ensure that the external auth link was created
				ea, err := app.FindFirstExternalAuthByExpr(dbx.HashExp{
					"provider":   "test",
					"providerId": "new_id",
				})
				if err != nil {
					t.Fatalf("Expected the external auth link to be created, got %v", err)
				}

				if ea.RecordRef() != "sywbhecnh46rhm0" {
					t.Fatalf("Expected the link to point to the test superuser, got %q", ea.RecordRef())
				}
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"record":{`,
				`"id":"sywbhecnh46rhm0"`,
				`"email":"test@example.com"`,
				`"token":"`,
				`"isNew":false`,
			},
			ExpectedEvents: map[string]int{
				"*": 0,
				// ---
				"OnRecordAuthWithOAuth2Request": 1,
				"OnRecordAuthRequest":           1,
				"OnRecordEnrich":                1,
				// ---
				// the new ExternalAuth link + the AuthOrigin of the login alert
				"OnModelCreate":              2,
				"OnModelCreateExecute":       2,
				"OnModelValidate":            2,
				"OnModelAfterCreateSuccess":  2,
				"OnRecordCreate":             2,
				"OnRecordCreateExecute":      2,
				"OnRecordValidate":           2,
				"OnRecordAfterCreateSuccess": 2,
				// ---
				"OnMailerRecordAuthAlertSend": 1,
				"OnMailerSend":                1,
			},
		},
		{
			Name:   "superuser IPs restriction is enforced",
			Method: http.MethodPost,
			URL:    "/api/collections/_superusers/auth-with-oauth2",
			Body: strings.NewReader(`{
				"provider":    "test",
				"code":        "123",
				"redirectURL": "https://example.com"
			}`),
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				superusersOAuth2Setup(t, app, &auth.AuthUser{Id: "test_id", Email: "test@example.com"})

				// precreate the link so that the sign-in resolves to a superuser record
				linkSuperuserOAuth2(t, app, "test@example.com", "test", "test_id")

				app.Settings().SuperuserIPs = []string{"1.2.3.4"}
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{`"data":{}`},
			ExpectedEvents: map[string]int{
				"*": 0,
				// ---
				"OnRecordAuthWithOAuth2Request": 1,
				// ---
				// the ExternalAuth link is precreated so nothing new is
				// saved and the IP check rejects before the auth response
			},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}

func TestSuperusersCollectionOAuth2Config(t *testing.T) {
	t.Parallel()

	app, _ := tests.NewTestApp()
	defer app.Cleanup()

	collection, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		t.Fatal(err)
	}

	collection.OAuth2.Enabled = true
	collection.OAuth2.Providers = []core.OAuth2ProviderConfig{{
		Name:         "gitlab",
		ClientId:     "123",
		ClientSecret: "456",
	}}

	if err = app.Save(collection); err != nil {
		t.Fatal(err)
	}

	reloaded, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		t.Fatal(err)
	}

	if !reloaded.OAuth2.Enabled {
		t.Fatal("Expected the superusers OAuth2 auth to be enabled")
	}

	config, ok := reloaded.OAuth2.GetProviderConfig("gitlab")
	if !ok {
		t.Fatal("Expected the gitlab provider config to be preserved")
	}

	if config.ClientId != "123" || config.ClientSecret != "456" {
		t.Fatalf("Unexpected provider config %+v", config)
	}

	// ensure that the mapped fields are still cleared
	// (the superusers collection has no custom fields)
	if reloaded.OAuth2.MappedFields.Name != "" {
		t.Fatalf("Expected the missing OAuth2 mapped fields to be unset, got %+v", reloaded.OAuth2.MappedFields)
	}
}
