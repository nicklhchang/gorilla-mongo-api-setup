package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const cookieCharacters = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

var randSeed *rand.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))

/*
Marshal encodes any Go value into a byte slice which represents JSON
Unmarshal decodes a byte slice (which is a JSON representation) into a Go value
Encoding and Decoding similar idea to Marshal and Unmarshal but implementation
isn't the exact same.

purpose of chaining middlewares; pass same w and r through multiple functions (handlers).
so call .ServeHTTP on next handler; ServeHTTP takes care of responding to a HTTP request.
*/
func AuthMiddleware(sCollection *mongo.Collection) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			ptrCookieSlice := r.Cookies()
			// fmt.Printf("cookies: %v\n", ptrCookieSlice)
			var sessionID string
			for _, ptrCookie := range ptrCookieSlice {
				if (*ptrCookie).Name == "session-id" {
					sessionID = (*ptrCookie).Value
				}
			}
			if len(sessionID) > 0 {
				exists, err := Exists(bson.D{{Key: "session", Value: sessionID}}, sCollection)
				if err != nil {
					log.Fatal(err) // something wrong with db
				}
				switch exists {
				case true:
					json.NewEncoder(w).Encode(fmt.Sprintf("session id: %s", sessionID))
					handler.ServeHTTP(w, r)
				case false:
					json.NewEncoder(w).Encode(fmt.Sprintf("session id: %s invalid", sessionID))
				}
			} else {
				json.NewEncoder(w).Encode("no session id, check your cookies")
			}
		})
	}
}

func Exists(searchParams bson.D, collection *mongo.Collection) (bool, error) {
	// no timeout context because NEED to find whether or not user or session exists
	var result bson.M
	err := collection.FindOne(context.TODO(), searchParams).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		log.Fatal(err)
		return false, err
	}
	// smoothly found: no ErrNoDocuments and unmarshaling by Decode went smooth
	return true, nil
}

// maybe sometime later use generics with Exist since so similar (FindOne())
func FindSession(searchSession bson.D, collection *mongo.Collection) (string, error) {
	// result will just be a map e.g. access username value by result["user"]
	var result bson.M
	err := collection.FindOne(context.TODO(), searchSession).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", nil
		}
		log.Fatal(err)
		return "", err
	}
	// assumes all values for key "session" will be stored as string
	return result["session"].(string), nil
}

// inserts doc into sessions collection. doc is the current session of authed user.
// is spawned as a goroutine and creates a session. then this goroutine will spawn
// another to delete session doc once timed out.
func CreateNewSession(channel chan<- string, userInfo map[string]string,
	sCollection *mongo.Collection) {
	// in future could consider maybe reddis or just a global slice for speed
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// empty initialisation rather than nil (var sessionDocument bson.M)
	sessionDocument := make(bson.M)
	sessionDocument["user"] = userInfo["user"]
	// generate a sessionID to be set in client's Cookie header
	sessionID := make([]byte, 64)
	for idx := range sessionID {
		sessionID[idx] = cookieCharacters[randSeed.Intn(len(cookieCharacters))]
	}
	sessionDocument["session"] = string(sessionID)
	// insert document (a session) into mongoDB
	_, err := sCollection.InsertOne(ctx, sessionDocument)
	if err != nil {
		fmt.Println("mongo error inserting new session document")
		return
	}
	// another goroutine so won't block the statement: channel <- string(sessionID)
	go DeleteSessionTimeout(sessionDocument, sCollection)
	channel <- string(sessionID)
}

// deletes session doc from mongo sessions collection. is spawned as a goroutine
// by createNewSession so that it can clean up after itself.
func DeleteSessionTimeout(session bson.M, sCollection *mongo.Collection) {
	timeoutChan := time.NewTimer(time.Duration(20 * time.Second))
	<-timeoutChan.C // halt execution of this goroutine here until timeout
	// doesn't matter how long it takes, session has to be deleted
	_, err := sCollection.DeleteOne(context.TODO(), session)
	if err != nil {
		fmt.Println("user session unable to be invalidated at this time")
	}
}

// for now almost same implementation as creating new session for authed user.
// just don't dispatch a deletion scheduled after 90 second
// no timeout on InsertOne either because important that user is registered in db
func CreateNewUser(channel chan<- *mongo.InsertOneResult, userInfo map[string]string,
	uCollection *mongo.Collection) {
	userDocument := make(bson.M)
	for keyField, valueUser := range userInfo {
		userDocument[keyField] = valueUser
	}
	newUser, err := uCollection.InsertOne(context.TODO(), userDocument)
	if err != nil {
		fmt.Println("mongo error inserting new user record")
	}
	channel <- newUser
}
