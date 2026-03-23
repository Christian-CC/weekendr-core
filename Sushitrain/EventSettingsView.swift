// Copyright (C) 2026 Christian Conrad
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.
import SwiftUI

struct EventSettingsView: View {
	@Environment(AppState.self) private var appState
	@Environment(\.showToast) private var showToast

	let eventID: String
	let apiClient: APIClient

	@State private var hostDeviceID: String? = nil
	@State private var joinLocked: Bool = false
	@State private var isUpdating: Bool = false
	@State private var isLoading: Bool = true

	/// True when the current device is the host of this specific event.
	private var isHost: Bool {
		guard let hostDeviceID else { return false }
		return hostDeviceID == appState.localDeviceID
	}

	var body: some View {
		Form {
			if isLoading {
				Section {
					ProgressView()
				}
			} else if isHost {
				Section {
					Toggle(isOn: $joinLocked) {
						VStack(alignment: .leading, spacing: 2) {
							Text("Block new joins")
							Text(joinLocked
								? "New joins are blocked."
								: "Anyone with the invite code can join")
								.font(.caption)
								.foregroundColor(.secondary)
						}
					}
					.disabled(isUpdating)
					.onChange(of: joinLocked) { _, newValue in
						Task {
							await updateJoinLocked(newValue)
						}
					}
				} header: {
					Text("ACCESS CONTROL")
				}
			}
		}
		.navigationTitle("Event Settings")
		.task {
			await loadEventDetail()
		}
	}

	private func loadEventDetail() async {
		defer { isLoading = false }
		do {
			let detail = try await apiClient.getEvent(eventID: eventID)
			hostDeviceID = detail.hostDeviceID
			joinLocked = detail.joinLocked
		} catch {
			// If we can't load, the section simply won't appear.
		}
	}

	private func updateJoinLocked(_ locked: Bool) async {
		isUpdating = true
		defer { isUpdating = false }

		do {
			let response = try await apiClient.setJoinLocked(
				eventID: eventID,
				deviceID: appState.localDeviceID,
				locked: locked
			)
			joinLocked = response.joinLocked
			showToast(Toast(
				title: locked ? "Joins blocked" : "Joins allowed",
				image: locked ? "lock.fill" : "lock.open.fill"
			))
		} catch {
			// Revert toggle on failure.
			joinLocked = !locked
			showToast(Toast(
				title: "Failed to update",
				image: "exclamationmark.triangle.fill",
				subtitle: "\(error.localizedDescription)"
			))
		}
	}
}
