package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type PaymentResult struct {
	TotalTip int64 `json:"total_tip"`
}

func GetPaymentResult(c echo.Context) error {
	ctx := c.Request().Context()
	_ = withResponseCache(ctx)

	return c.JSON(http.StatusOK, &PaymentResult{
		TotalTip: statsPaymentTotalTips.Load(),
	})
}
