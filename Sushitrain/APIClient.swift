// Copyright (C) 2026 Christian Conrad
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.
import Foundation

enum APIError: Error, LocalizedError {
	case httpError(Int, APIErrorBody?)
	case networkError(Error)
	case decodingError

	var errorDescription: String? {
		switch self {
		case .httpError(_, let body):
			return body?.message ?? "An unknown error occurred."
		case .networkError(let error):
			return error.localizedDescription
		case .decodingError:
			return "Failed to decode server response."
		}
	}
}

struct APIErrorBody: Codable {
	let error: String
	let message: String
}

struct JoinLockedResponse: Codable {
	let status: String
	let joinLocked: Bool
}

struct InviteResponse: Codable {
	let eventID: String
	let name: String
	let hostDeviceID: String
	let state: String
}

struct EventDetailResponse: Codable {
	let eventID: String
	let name: String
	let hostDeviceID: String
	let state: String
	let joinLocked: Bool
}

class APIClient {
	let baseURL: URL

	init(baseURL: URL) {
		self.baseURL = baseURL
	}

	// MARK: - Event Detail

	func getEvent(eventID: String) async throws -> EventDetailResponse {
		let url = baseURL.appendingPathComponent("events/\(eventID)")
		var request = URLRequest(url: url)
		request.httpMethod = "GET"

		let (data, response) = try await URLSession.shared.data(for: request)

		guard let httpResponse = response as? HTTPURLResponse else {
			throw APIError.decodingError
		}

		if httpResponse.statusCode != 200 {
			let errorBody = try? JSONDecoder().decode(APIErrorBody.self, from: data)
			throw APIError.httpError(httpResponse.statusCode, errorBody)
		}

		guard let result = try? JSONDecoder().decode(EventDetailResponse.self, from: data) else {
			throw APIError.decodingError
		}
		return result
	}

	// MARK: - Join Locking

	func setJoinLocked(eventID: String, deviceID: String, locked: Bool) async throws -> JoinLockedResponse {
		let url = baseURL.appendingPathComponent("events/\(eventID)")
		var request = URLRequest(url: url)
		request.httpMethod = "PATCH"
		request.setValue("application/json", forHTTPHeaderField: "Content-Type")
		request.setValue(deviceID, forHTTPHeaderField: "X-Device-ID")
		request.httpBody = try JSONEncoder().encode(["joinLocked": locked])

		let (data, response) = try await URLSession.shared.data(for: request)

		guard let httpResponse = response as? HTTPURLResponse else {
			throw APIError.decodingError
		}

		if httpResponse.statusCode != 200 {
			let errorBody = try? JSONDecoder().decode(APIErrorBody.self, from: data)
			throw APIError.httpError(httpResponse.statusCode, errorBody)
		}

		guard let result = try? JSONDecoder().decode(JoinLockedResponse.self, from: data) else {
			throw APIError.decodingError
		}
		return result
	}

	// MARK: - Invite

	func getInvite(secret: String) async throws -> InviteResponse {
		let url = baseURL.appendingPathComponent("invite/\(secret)")
		var request = URLRequest(url: url)
		request.httpMethod = "GET"

		let (data, response) = try await URLSession.shared.data(for: request)

		guard let httpResponse = response as? HTTPURLResponse else {
			throw APIError.decodingError
		}

		if httpResponse.statusCode != 200 {
			let errorBody = try? JSONDecoder().decode(APIErrorBody.self, from: data)
			throw APIError.httpError(httpResponse.statusCode, errorBody)
		}

		guard let result = try? JSONDecoder().decode(InviteResponse.self, from: data) else {
			throw APIError.decodingError
		}
		return result
	}
}
