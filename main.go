package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joho/godotenv"
)

var (
	TOPIK_LOG    = "ptpn/log_sensor"
	lastSentTime time.Time
)

var client mqtt.Client

type FirebasePayload struct {
	SensorID    string  `json:"sensorId"`
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	Timestamp   string  `json:"timestamp"`
}

type Record struct {
	SensorID string    `db:"sensor_id" json:"sensorId"`
	Suhu     float64   `db:"suhu" json:"suhu"`
	Humidity float64   `db:"humidity" json:"humidity"`
	Status   bool      `db:"status" json:"status"`
	DateTime time.Time `db:"date_time" json:"dateTime"`
}

type SensorHF struct {
	Conn     net.UDPConn
	IDSensor []byte
}

// Fungsi untuk menghitung CRC-16 Modbus
func calculateCRC(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc >>= 1
				crc ^= 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func sendUDPData(data []byte, udpAddr string) error {
	// Alamat tujuan untuk meneruskan respons ke localhost:5005
	forwardAddr, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		log.Printf("Error resolving forward address: %v\n", err)
	}

	// Membuat koneksi untuk meneruskan respons
	forwardConn, err := net.DialUDP("udp", nil, forwardAddr)
	if err != nil {
		log.Printf("Error creating forward connection: %v\n", err)
	}
	defer forwardConn.Close()

	// Meneruskan respons ke localhost:5005
	_, err = forwardConn.Write(data)
	if err != nil {
		log.Printf("Error forwarding response: %v\n", err)
	}
	fmt.Printf("Response forwarded to %s: %x\n", forwardAddr.String(), data)
	return err
}

func sendDataToAPI(temperature, humidity float64, sensorID string) error {
	apiURL := "http://localhost:3000/api/sensor" // URL Node.js API
	payload := fmt.Sprintf(`{"sensor_id": "%s", "temperature": %.2f, "humidity": %.2f}`, sensorID, temperature, humidity)
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer([]byte(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send data: status code %d", resp.StatusCode)
	}
	return nil
}

// func sendToWhatsApp(sensorID string, fTemperature, fHumidity float64, message string) error {
// 	apiURL := "http://localhost:3000/api/sensor" // Sesuaikan dengan API Node.js
// 	payload := map[string]interface{}{
// 		"sensor_id":   sensorID,
// 		"temperature": fTemperature,
// 		"humidity":    fHumidity,
// 		"message":     message,
// 	}

// 	jsonData, err := json.Marshal(payload)
// 	if err != nil {
// 		return fmt.Errorf("failed to marshal JSON: %v", err)
// 	}

// 	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
// 	if err != nil {
// 		return fmt.Errorf("failed to create request: %v", err)
// 	}
// 	req.Header.Set("Content-Type", "application/json")

// 	client := &http.Client{}
// 	resp, err := client.Do(req)
// 	if err != nil {
// 		return fmt.Errorf("failed to send request: %v", err)
// 	}
// 	defer resp.Body.Close()

// 	if resp.StatusCode != http.StatusOK {
// 		return fmt.Errorf("failed to send data, status code: %d", resp.StatusCode)
// 	}

// 	log.Println("WhatsApp notification sent successfully!")
// 	return nil
// }

func sendDataToFirebase(sensorID string, temperature, humidity float64) error {
	firebaseURL := "https://envirolink-5b459-default-rtdb.firebaseio.com/sensors/" + sensorID + ".json"

	payload := FirebasePayload{
		SensorID:    sensorID,
		Temperature: temperature,
		Humidity:    humidity,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize payload: %w", err)
	}

	req, err := http.NewRequest("PUT", firebaseURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to Firebase: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send data to Firebase, status code: %d", resp.StatusCode)
	}

	log.Printf("Data sent to Firebase: %+v\n", payload)
	return nil
}

func main() {
	// Threshold untuk kondisi suhu dan kelembapan
	temperatureThreshold := 40.0
	humidityThreshold := 80.0
	// Interval pengiriman minimal
	alertInterval := 1 * time.Minute //1 mnt, bisa pake time.Hour atau time.Second

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	BROKER_HOST := os.Getenv("BROKER_HOST")
	BROKER_USERNAME := os.Getenv("BROKER_USERNAME")
	BROKER_PASSWORD := os.Getenv("BROKER_PASSWORD")
	BROKER_CLIEN_ID := os.Getenv("BROKER_CLIEN_ID")

	TOPIK_LOG = os.Getenv("TOPIK_LOG")
	// Configure client options
	opts := mqtt.NewClientOptions().
		AddBroker(BROKER_HOST).
		SetClientID(BROKER_CLIEN_ID).
		SetAutoReconnect(true).
		SetCleanSession(true)
	opts.SetUsername(BROKER_USERNAME)
	opts.SetPassword(BROKER_PASSWORD)

	//dimatikan agar bisa diuploud
	// Create and start a client using the above ClientOptions
	// client = mqtt.NewClient(opts)
	// if token := client.Connect(); token.Wait() && token.Error() != nil {
	// 	log.Fatalf("Failed to connect to broker: %v", token.Error())
	// }

	TimeOut := 500 * time.Millisecond
	// Alamat UDP Server
	remoteAddr, err := net.ResolveUDPAddr("udp", "192.168.10.180:9999") //UDP HF2211A
	if err != nil {
		log.Fatalf("Error resolving remote address: %v\n", err)

	}

	// Alamat lokal untuk menerima respons
	localAddr, err := net.ResolveUDPAddr("udp", "0.0.0.0:0")
	if err != nil {
		log.Fatalf("Error resolving local address: %v\n", err)
	}

	// Membuat koneksi UDP
	conn, err := net.DialUDP("udp", localAddr, remoteAddr)
	if err != nil {
		log.Fatalf("Error creating UDP connection: %v\n", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // Timeout 2 detik untuk menerima respons

	tempHumAddr := []SensorHF{{IDSensor: []byte{135}, Conn: *conn}} // contoh 01 Humidity & Temperatur Sensor, 08 high temperature sensor
	sendTH := []byte{0x03, 0x00, 0x00, 0x00, 0x02}                  //Command hexadecimal

	ticker := time.NewTicker(TimeOut)
	defer ticker.Stop()

	for range ticker.C {
		for _, sensorHF := range tempHumAddr {
			for _, addrSensor := range sensorHF.IDSensor {
				var dataToSend []byte
				if addrSensor < 100 {
				} else {
					dataToSend = append([]byte{addrSensor}, sendTH...)
				}
				crc := calculateCRC(dataToSend)
				crcBytes := make([]byte, 2)
				binary.LittleEndian.PutUint16(crcBytes, crc)
				dataToSend = append(dataToSend, crcBytes...)
				fmt.Println("add:", addrSensor)
				fmt.Println("conn:", sensorHF.Conn.RemoteAddr())
				fmt.Println("dataToSend:", dataToSend)

				// Mengirim data ke server
				_, err = sensorHF.Conn.Write(dataToSend)
				if err != nil {
					log.Println("Error sending data: %v\n", err)
				}

				// Menerima respons dari server
				buf := make([]byte, 32)
				sensorHF.Conn.SetReadDeadline(time.Now().Add(TimeOut)) // Timeout 2 detik untuk menerima respons
				n, _, err := sensorHF.Conn.ReadFromUDP(buf)
				if err != nil {
					log.Printf("Error receiving response: %v\n", err)

				}

				var hum, temp int32
				if n >= 5 {
					if addrSensor > 100 {
						hum = int32(buf[3])<<8 | int32(buf[4])
						temp = int32(buf[5])<<8 | int32(buf[6])
					} else {
						temp = int32(buf[3])<<8 | int32(buf[4])
						hum = 0

					}

					fHumidity := float64(hum) / 10
					fTemperature := float64(temp) / 10
					fmt.Printf("ID: %d ", addrSensor)
					fmt.Printf("Suhu: %f ", fTemperature)
					fmt.Printf("Kelembaban: %f", fHumidity)
					fmt.Println()
					var sensorId string
					sensorId += fmt.Sprintf("%d", addrSensor)
					if err != nil {
						fmt.Println("error on :", err)
					}

					// if err := pubEvent(client, record, TOPIK_LOG); err != nil {

					// 	fmt.Printf("Publish gagal: %v\n", err)
					// 	record.Status = false
					// 	return
					// }

					err = sendDataToFirebase(fmt.Sprintf("%d", addrSensor), fTemperature, fHumidity)
					if err != nil {
						log.Printf("Failed to send data to Firebase: %v\n", err)
					} else {
					}

					if fTemperature >= temperatureThreshold || fHumidity >= humidityThreshold {
						if time.Since(lastSentTime) >= alertInterval {
							err := sendDataToAPI(fTemperature, fHumidity, sensorId)
							lastSentTime = time.Now()
							if err != nil {
								log.Printf("Failed to send sensor data to API: %v", err)
							}
						} else {
							log.Println("Alert condition met, but waiting for alert interval.")
						}
					} else {
						log.Println("Conditions are normal, no alert sent.")
					}
				}
			}
		}
	}
}
