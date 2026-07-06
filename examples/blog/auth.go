package blog

import (
	"time"

	"github.com/Newton-School/gogo/auth"
)

func StaffUser() auth.User {
	contentType := auth.ContentType{ID: 1, AppLabel: "blog", Model: "post"}
	return auth.User{
		AbstractUser: auth.AbstractUser{
			AbstractBaseUser: auth.AbstractBaseUser{
				ID:            1,
				IsSuperuser:   false,
				IsActive:      true,
				DateJoined:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Authenticated: true,
				Groups: []auth.Group{
					{
						ID:   1,
						Name: "Editors",
						Permissions: []auth.Permission{
							{Name: "Can view post", ContentTypeID: contentType.ID, ContentType: contentType, Codename: "view_post", AppLabel: "blog"},
							{Name: "Can change post", ContentTypeID: contentType.ID, ContentType: contentType, Codename: "change_post", AppLabel: "blog"},
						},
					},
				},
				UserPermissions: []auth.Permission{
					{Name: "Can publish post", ContentTypeID: contentType.ID, ContentType: contentType, Codename: "publish_post", AppLabel: "blog"},
				},
			},
			Username: "blog-editor",
			Email:    "editor@example.com",
			IsStaff:  true,
		},
	}
}
