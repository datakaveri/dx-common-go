package model

// DxUser is the platform user model persisted to the database.
// For the in-context identity after JWT validation, use auth.DxUser instead.
type DxUser struct {
	ID               string `db:"id"               json:"id"`
	Email            string `db:"email"            json:"email"`
	Name             string `db:"name"             json:"name"`
	OrganisationID   string `db:"organisation_id"  json:"organisationId"`
	OrganisationName string `db:"organisation_name" json:"organisationName"`
	Status           string `db:"status"           json:"status"`
	CreatedAt        string `db:"created_at"       json:"createdAt"`
	UpdatedAt        string `db:"updated_at"       json:"updatedAt"`
}
