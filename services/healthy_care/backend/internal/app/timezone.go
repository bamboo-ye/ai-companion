package app

import (
	"context"
	"time"
)

func userLocation(ctx context.Context, q querier, user string) (*time.Location, error) {
	values, err := rows(ctx, q, "SELECT data->>'timezone' AS timezone FROM profiles WHERE user_id=$1", user)
	if err != nil {
		return nil, err
	}
	zone := "Asia/Shanghai"
	if len(values) > 0 {
		if value, ok := values[0]["timezone"].(string); ok && value != "" {
			zone = value
		}
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.LoadLocation("Asia/Shanghai")
	}
	return loc, nil
}
