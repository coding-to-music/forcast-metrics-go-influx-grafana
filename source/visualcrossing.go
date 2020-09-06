package source

import (
	"encoding/json"
	"github.com/cenkalti/backoff/v3"
	"github.com/pkg/errors"
	"github.com/tedpearson/weather2influxdb/convert"
	"github.com/tedpearson/weather2influxdb/http"
	"github.com/tedpearson/weather2influxdb/weather"
	"log"
	"math"
	"net/url"
	"time"
)

type VisualCrossing struct {
	Key string
}

func (v VisualCrossing) GetWeather(lat string, lon string, retryer http.Retryer) ([]weather.Record, error) {
	// location can be lat,lon in decimal degrees!
	// alertLevel=summary for alerts
	base := "https://weather.visualcrossing.com/VisualCrossingWebServices/rest/services/weatherdata/forecast?"
	q := url.Values{}
	q.Add("aggregateHours", "1")
	q.Add("contentType", "json")
	q.Add("unitGroup", "us")
	q.Add("locationMode", "single")
	q.Add("key", v.Key) // todo
	q.Add("location", lat+","+lon)
	q.Add("includeAstronomy", "true")
	off := backoff.NewExponentialBackOff()
	// note: low number of retries because we are using free tier (250 results/day)
	off.MaxElapsedTime = 4 * time.Second
	log.Println("Getting VisualCrossing forecast")
	body, err := retryer.RetryRequest(base+q.Encode(), off)
	if err != nil {
		return nil, err
	}
	defer cleanup(body)

	var forecast vcForecast
	err = json.NewDecoder(body).Decode(&forecast)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	records, err := v.transformForecast(forecast.Location.Values)
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (v VisualCrossing) transformForecast(measurements []vcMeasurement) ([]weather.Record, error) {
	// stop when things become null. (maybe, or maybe we skip null points)
	values := make([]weather.Record, 0, len(measurements))
	var lastAstro time.Time
	for _, m := range measurements {
		// note: after 7 days, the forecast data is every 3 hours
		//       but the other 2 hours are still in the output
		//       with null values for everything except precip/datetime/datetimeStr
		//       therefore it is important to skip these, which all have null temps.
		if m.Temp == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, m.DatetimeStr)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		wdir := int(*m.Wdir)
		skyCover := convert.PercentToRatio(*m.CloudCover)
		precipProb := convert.PercentToRatio(*m.Pop)
		record := weather.Record{
			Time:                     t,
			Temperature:              m.Temp,
			Dewpoint:                 calcDewpoint(*m.Humidity, *m.Temp),
			FeelsLike:                feelsLike(m.Temp, m.HeatIndex, m.WindChill),
			SkyCover:                 &skyCover,
			WindDirection:            &wdir,
			WindSpeed:                m.Wspd,
			WindGust:                 m.Wgust,
			PrecipitationProbability: &precipProb,
			PrecipitationAmount:      m.Precip,
			SnowAmount:               convert.NilToZero(m.Snow),
		}
		if lastAstro.Day() != t.Day() {
			lastAstro = t
			// do sunrise/sunset
			stamp, err := time.Parse(time.RFC3339, *m.Sunrise)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			one := 1
			zero := 0
			sunrise := weather.Record{
				Time:  stamp,
				SunUp: &one,
			}
			stamp, err = time.Parse(time.RFC3339, *m.Sunset)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			// visualcrossing moon phase:
			// 0   = new moon
			// 0.5 = full moon
			// 1   = new moon again
			moonRatio := convert.Round(2.0*math.Abs(*m.MoonPhase-0.5), 2)
			sunset := weather.Record{
				Time:          stamp,
				SunUp:         &zero,
				FullMoonRatio: &moonRatio,
			}
			values = append(values, sunrise)
			values = append(values, sunset)
		}
		values = append(values, record)
	}
	return values, nil
}

func feelsLike(temp *float64, heatIndex *float64, windChill *float64) *float64 {
	if windChill != nil {
		return windChill
	}
	if heatIndex != nil {
		return heatIndex
	}
	return temp
}

func calcDewpoint(rh float64, tempF float64) *float64 {
	tempC := convert.FToC(tempF)
	dpC := (237.3 * (math.Log(rh/100) + ((17.27 * tempC) / (237.3 + tempC)))) /
			(17.27 - (math.Log(rh/100) + ((17.27 * tempC) / (237.3 + tempC))))
	f := convert.CToF(dpC)
	return &f
}

type vcMeasurement struct {
	Wdir        *float64 `json:"wdir"`
	Temp        *float64 `json:"temp"`
	Sunrise     *string  `json:"sunrise"`
	Wspd        *float64 `json:"wspd"`
	DatetimeStr string   `json:"datetimeStr"`
	HeatIndex   *float64 `json:"heatindex"`
	Humidity    *float64 `json:"humidity"`
	CloudCover  *float64 `json:"cloudcover"`
	Pop         *float64 `json:"pop"`
	Datetime    int64    `json:"datetime"`
	Precip      *float64 `json:"precip"`
	Snow        *float64 `json:"snow"`
	Sunset      *string  `json:"sunset"`
	Wgust       *float64 `json:"wgust"`
	WindChill   *float64 `json:"windchill"`
	MoonPhase   *float64 `json:"moonphase"`
}

type vcForecast struct {
	Location struct {
		Values []vcMeasurement `json:"values"`
	} `json:"location"`
}
