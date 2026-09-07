package middleware

import "github.com/labstack/echo/v5"

// NewErrorHandler renders returned errors before outer middleware resumes.
func NewErrorHandler() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if err := next(c); err != nil {
				c.Echo().HTTPErrorHandler(c, err)
			}
			return nil
		}
	}
}
