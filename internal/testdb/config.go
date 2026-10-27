package testdb

import (
	"errors"
	"net/url"
)

// ValidateURL prevents integration fixtures from targeting the demo database.
func ValidateURL(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() == "" || u.Path != "/transfer_test" || u.User == nil ||
		u.User.Username() != "transfer_test" || u.Fragment != "" {
		return "", errors.New("TEST_DATABASE_URL must target database transfer_test as role transfer_test; demo URLs are forbidden")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("TEST_DATABASE_URL query is invalid")
	}
	for key := range query {
		if key != "sslmode" && key != "connect_timeout" {
			return "", errors.New("TEST_DATABASE_URL only allows sslmode and connect_timeout options; connection overrides are forbidden")
		}
	}
	query.Set("connect_timeout", "3")
	u.RawQuery = query.Encode()
	return u.String(), nil
}
