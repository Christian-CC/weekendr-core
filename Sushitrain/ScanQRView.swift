// Copyright (C) 2026 Christian Conrad
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.
import SwiftUI

struct ScanQRView: View {
	@Environment(\.dismiss) private var dismiss

	let apiClient: APIClient

	@State private var scannedSecret: String? = nil
	@State private var joinError: JoinError? = nil
	@State private var inviteResponse: InviteResponse? = nil
	@State private var isLoading: Bool = false

	enum JoinError {
		case eventEnded
		case collectionExpired
		case joinLocked
		case generic(String)

		var icon: String {
			switch self {
			case .eventEnded: return "checkmark.circle"
			case .collectionExpired: return "hourglass"
			case .joinLocked: return "lock.fill"
			case .generic: return "exclamationmark.triangle"
			}
		}

		var title: String {
			switch self {
			case .eventEnded: return "Event has ended"
			case .collectionExpired: return "Collection window closed"
			case .joinLocked: return "Event is locked"
			case .generic(let msg): return msg
			}
		}

		var body: String {
			switch self {
			case .eventEnded:
				return "This event is no longer accepting members."
			case .collectionExpired:
				return "The photo collection period for this event has ended."
			case .joinLocked:
				return "The host has closed this event to new members."
			case .generic:
				return "Something went wrong. Please try again."
			}
		}
	}

	var body: some View {
		NavigationStack {
			VStack(spacing: 24) {
				if isLoading {
					ProgressView("Checking invite...")
				} else if let error = joinError {
					errorCard(error)
				} else if inviteResponse != nil {
					// Successful invite — show join UI
					Text("Ready to join!")
						.font(.title2)
						.fontWeight(.semibold)
					Button("Join Event") {
						// TODO: Wire to JoinEvent flow
						dismiss()
					}
					.buttonStyle(.borderedProminent)
					.tint(.orange)
				} else {
					// Placeholder for camera/QR scanner
					VStack(spacing: 16) {
						Image(systemName: "qrcode.viewfinder")
							.font(.system(size: 80))
							.foregroundColor(.secondary)
						Text("Scan an invite QR code")
							.font(.headline)
							.foregroundColor(.secondary)
					}
					.frame(maxHeight: .infinity)
				}
			}
			.padding()
			.navigationTitle("Join Event")
			.toolbar {
				ToolbarItem(placement: .cancellationAction) {
					Button("Cancel") { dismiss() }
				}
			}
		}
	}

	@ViewBuilder
	private func errorCard(_ error: JoinError) -> some View {
		VStack(spacing: 16) {
			Image(systemName: error.icon)
				.font(.system(size: 48))
				.foregroundColor(.orange)

			Text(error.title)
				.font(.title3)
				.fontWeight(.semibold)

			Text(error.body)
				.font(.body)
				.foregroundColor(.secondary)
				.multilineTextAlignment(.center)
		}
		.padding(32)
		.frame(maxWidth: .infinity)
		.background(Color.secondary.opacity(0.08))
		.cornerRadius(16)
	}

	/// Call this when a QR code is scanned with the invite secret.
	func handleScannedSecret(_ secret: String) {
		scannedSecret = secret
		joinError = nil
		inviteResponse = nil
		isLoading = true

		Task {
			do {
				let response = try await apiClient.getInvite(secret: secret)
				inviteResponse = response
			} catch let apiError as APIError {
				switch apiError {
				case .httpError(403, let body):
					switch body?.error {
					case "event_ended":
						joinError = .eventEnded
					case "collection_expired":
						joinError = .collectionExpired
					case "join_locked":
						joinError = .joinLocked
					default:
						joinError = .generic(body?.message ?? "Access denied.")
					}
				default:
					joinError = .generic(apiError.localizedDescription)
				}
			} catch {
				joinError = .generic(error.localizedDescription)
			}
			isLoading = false
		}
	}
}
