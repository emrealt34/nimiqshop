package main

import (
	"encoding/json"
	"fmt"
	"nimiqshop/internal/cryptorefills"
)

const sample = `[{"country_code":"TR","rich_description":{"markup":"html","description":"<p>desc</p>","how_to_redeem":"<ol><li>x</li></ol>","redeem_geo":"in TR"},"products":[]}]`

func main() {
	var fams []cryptorefills.Family
	if err := json.Unmarshal([]byte(sample), &fams); err != nil {
		fmt.Println("unmarshal err:", err)
		return
	}
	if fams[0].RichDescription == nil {
		fmt.Println("RichDescription: NIL - decode basarisiz")
	} else {
		fmt.Println("RichDescription OK:", fams[0].RichDescription.HowToRedeem)
	}
}
