package lib

import "context"

// Status returns the current status of the DB manager component
func (d *DBManagerComponent) Status(ctx context.Context) map[string]interface{} {
	status := make(map[string]interface{})

	if d.dbManager != nil {
		status["db_manager"] = d.dbManager.Status(ctx)
	} else {
		status["db_manager"] = nil
	}

	return status
}
