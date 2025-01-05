package cron

import (
	"time"

	"github.com/lasikuu/GinBot/pkg/cron/cronjob"
)

func RunCronJobs() {
	ticker := time.NewTicker(1 * time.Second)

	started := time.Now()
	for now := range ticker.C {

		cronjob.Remind()

		// Every minute
		if now.Second() == 0 {
			cronjob.CongratulateBirthday()
		}

		// Every hour
		if now.Minute() == 0 && now.Second() == 0 {
		}

		if now.Sub(started) >= time.Hour*24 {
			// Resets the ticker after 24 hours
			ticker.Reset(1 * time.Second)
		}
	}
}
