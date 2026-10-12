package database

import "database/sql"

func readSchedulerOutboxEvents(rows *sql.Rows, capacity int) ([]SchedulerOutboxEvent, error) {
	events := []SchedulerOutboxEvent{}
	for rows.Next() {
		var event SchedulerOutboxEvent
		var created any
		if err := rows.Scan(&event.ID, &event.EntityType, &event.EntityID, &event.EventType, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = parseDBTimeValue(created)
		// 空轮询不分配整批结果；有事件时仍一次分配完整容量。
		if len(events) == 0 {
			events = make([]SchedulerOutboxEvent, 0, capacity)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
