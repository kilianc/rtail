/*!
 * Test hooks. Only compiled into tests.
 */

package catalog

import "database/sql"

// DBForTest exposes the database, so a test can put a catalog into a state an
// earlier release left behind.
func (c *Catalog) DBForTest() *sql.DB { return c.db }
